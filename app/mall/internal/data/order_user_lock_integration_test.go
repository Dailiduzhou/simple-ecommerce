//go:build integration

package data

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type orderUserLockBarrier struct {
	loaded    chan struct{}
	release   chan struct{}
	deletePID chan uint32
}

type orderUserLockQuerier struct {
	db.Querier
	tx      pgx.Tx
	barrier *orderUserLockBarrier
}

func (q orderUserLockQuerier) pause(ctx context.Context) error {
	close(q.barrier.loaded)
	select {
	case <-q.barrier.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q orderUserLockQuerier) GetShippingAddressForSnapshot(ctx context.Context, args db.GetShippingAddressForSnapshotParams) (db.ShippingAddress, error) {
	row, err := q.Querier.GetShippingAddressForSnapshot(ctx, args)
	if err == nil {
		err = q.pause(ctx)
	}
	return row, err
}

func (q orderUserLockQuerier) GetOrderForUpdate(ctx context.Context, id int64) (db.Order, error) {
	row, err := q.Querier.GetOrderForUpdate(ctx, id)
	if err == nil {
		err = q.pause(ctx)
	}
	return row, err
}

func (q orderUserLockQuerier) GetOrderForUpdateByPaymentID(ctx context.Context, id int64) (db.Order, error) {
	row, err := q.Querier.GetOrderForUpdateByPaymentID(ctx, id)
	if err == nil {
		err = q.pause(ctx)
	}
	return row, err
}

func (q orderUserLockQuerier) LockCommunityUser(ctx context.Context, id int64) (db.User, error) {
	// Signal before taking the deletion lock: fixed order writers make deletion
	// wait here, while the previous lock order let it reach the cascading DELETE.
	q.barrier.deletePID <- q.tx.Conn().PgConn().PID()
	return q.Querier.LockCommunityUser(ctx, id)
}

type orderUserLockTx struct {
	biz.TxManager
	barrier *orderUserLockBarrier
}

func (tx orderUserLockTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		pgTx := pgTxFromContext(ctx)
		q := orderUserLockQuerier{querierFromContext(ctx, nil), pgTx, tx.barrier}
		return fn(WithQuerier(ctx, q, pgTx))
	})
}

func TestOrderWritesSerializeWithAccountDeletionIntegration(t *testing.T) {
	for _, operation := range []string{"checkout", biz.OrderActionShip, biz.OrderActionComplete, "prepare_refund", biz.ReconciliationActionRetry, biz.ReconciliationActionResolve} {
		t.Run(operation, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			barrier := &orderUserLockBarrier{
				loaded: make(chan struct{}), release: make(chan struct{}), deletePID: make(chan uint32, 1),
			}
			var once sync.Once
			release := func() { once.Do(func() { close(barrier.release) }) }
			var workers sync.WaitGroup
			defer func() {
				release()
				cancel()
				workers.Wait()
			}()
			tx := orderUserLockTx{f.tx, barrier}
			repo := NewOrderRepoWithJobs(f.data, tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
			var orderID, paymentID int64
			if operation != "checkout" {
				orderID, paymentID = fulfillmentOrder(t, f)
				if operation == biz.OrderActionComplete {
					_, err := f.pool.Exec(ctx, `UPDATE orders SET status='shipped' WHERE id=$1`, orderID)
					require.NoError(t, err)
				}
			}
			reconciliation := operation == biz.ReconciliationActionRetry || operation == biz.ReconciliationActionResolve
			if reconciliation {
				_, err := f.pool.Exec(ctx, `UPDATE payments SET third_party_tx_id=$2 WHERE id=$1`, paymentID, f.prefix+"_verified_tx")
				require.NoError(t, err)
				payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
				require.NoError(t, payments.MarkReconciliationRequired(ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "job_exhausted", LastError: "timeout"}))
			}
			operationDone := make(chan error, 1)
			workers.Go(func() {
				var err error
				if operation == "checkout" {
					_, err = repo.CreateOrder(ctx, biz.CreateOrderArgs{
						UserID: f.userID, AddressID: f.addressID, OutTradeNo: f.prefix + "_checkout",
						Currency: "CNY", Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}},
						IdempotencyKey: f.prefix + "_checkout", RequestHash: strings.Repeat("a", 64),
					})
				} else if reconciliation {
					reconciliationRepo := NewPaymentReconciliationRepo(f.data, tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
					_, err = reconciliationRepo.ApplyReconciliation(ctx, biz.Actor{ID: f.userID, Admin: true}, reconciliationInput(paymentID, 1, operation, "delete-overlap"))
				} else if operation == "prepare_refund" {
					payments := NewPaymentRepo(f.data, tx, log.DefaultLogger)
					_, _, err = payments.PreparePaymentRefund(ctx, paymentID, f.prefix+"_refund")
				} else {
					input := shipmentInput(orderID)
					if operation == biz.OrderActionComplete {
						input = biz.OrderFulfillmentInput{OrderID: orderID, Action: operation, IdempotencyKey: "complete-001", Reason: "received"}
					}
					_, err = repo.ApplyFulfillment(ctx, biz.Actor{ID: f.userID, Admin: operation == biz.OrderActionShip}, input)
				}
				operationDone <- err
			})
			select {
			case <-barrier.loaded:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			deletionDone := make(chan error, 1)
			users := NewCommunityUserRepo(f.data, tx, nil, log.DefaultLogger)
			workers.Go(func() { deletionDone <- users.DeleteUser(ctx, f.userID) })
			var deletePID uint32
			select {
			case deletePID = <-barrier.deletePID:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.Eventually(t, func() bool {
				var blocked bool
				err := f.pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(deletePID)).Scan(&blocked)
				return err == nil && blocked
			}, 3*time.Second, 10*time.Millisecond, "account deletion must overlap the order writer's row locks")
			release()
			select {
			case err := <-operationDone:
				require.NoError(t, err, "account deletion must not deadlock the order writer")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-deletionDone:
				require.Error(t, err)
				require.Equal(t, int32(409), kerrors.FromError(err).Code)
				require.Equal(t, "ACCOUNT_HAS_RETAINED_HISTORY", kerrors.FromError(err).Reason)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			_, err := f.data.DB(ctx).GetUserByID(ctx, f.userID)
			require.NoError(t, err, "rejected deletion must preserve the account")
			var stock int32
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
			require.Equal(t, int32(99), stock)
			if operation == "checkout" {
				row, err := f.data.DB(ctx).GetOrderByUserIdempotency(ctx, db.GetOrderByUserIdempotencyParams{UserID: f.userID, IdempotencyKey: f.prefix + "_checkout"})
				require.NoError(t, err)
				require.Equal(t, biz.OrderStatusPendingPayment, row.Status)
			} else if reconciliation {
				actions, err := f.data.DB(ctx).ListReconciliationActions(ctx, db.ListReconciliationActionsParams{PaymentID: paymentID, Limit: 10})
				require.NoError(t, err)
				require.Len(t, actions, 1)
				require.Equal(t, operation, actions[0].Action)
			} else if operation == "prepare_refund" {
				row, err := f.data.DB(ctx).GetOrder(ctx, orderID)
				require.NoError(t, err)
				require.Equal(t, biz.OrderStatusPaid, row.Status, "preparing a refund does not settle it")
				payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
				payment, refund, err := payments.PreparePaymentRefund(ctx, paymentID, f.prefix+"_replay")
				require.NoError(t, err)
				require.Equal(t, biz.PaymentStatusSuccess, payment.Status)
				require.Equal(t, biz.PaymentRefundStatusPending, refund.Status)
				require.Equal(t, biz.RefundOrderCancel, refund.Purpose)
				require.Equal(t, f.prefix+"_refund", refund.OutRefundNo, "replay preserves the original refund")
				var count int
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM order_refunds WHERE payment_id=$1`, paymentID).Scan(&count))
				require.Equal(t, 1, count)
			} else {
				row, err := f.data.DB(ctx).GetOrder(ctx, orderID)
				require.NoError(t, err)
				if operation == biz.OrderActionShip {
					require.Equal(t, biz.OrderStatusShipped, row.Status)
				} else {
					require.Equal(t, biz.OrderStatusCompleted, row.Status)
				}
				actions, err := f.data.DB(ctx).ListOrderFulfillmentActions(ctx, orderID)
				require.NoError(t, err)
				require.Len(t, actions, 1)
				require.Equal(t, operation, actions[0].Action)
			}
		})
	}
}
