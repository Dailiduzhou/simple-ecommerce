//go:build integration

package data

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func fulfillmentOrder(t *testing.T, f *correctnessFixture) (int64, int64) {
	t.Helper()
	orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
	_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status='paid',paid_payment_id=$2 WHERE id=$1`, orderID, paymentID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_minor,product_name_snapshot,cover_image_snapshot) VALUES($1,$2,1,12345,'product','[]');`, orderID, f.productID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=stock-1 WHERE id=$1`, f.productID)
	require.NoError(t, err)
	return orderID, paymentID
}

func shipmentInput(orderID int64) biz.OrderFulfillmentInput {
	return biz.OrderFulfillmentInput{OrderID: orderID, Action: biz.OrderActionShip, IdempotencyKey: "shipment-001", Reason: "warehouse dispatched", Carrier: "carrier", TrackingNumber: "tracking-001"}
}

func TestReviewFulfillmentLifecycleAndIdempotencyIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID := fulfillmentOrder(t, f)
	repo := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
	admin, owner := biz.Actor{ID: f.userID, Admin: true}, biz.Actor{ID: f.userID}
	ship := shipmentInput(orderID)
	complete := biz.OrderFulfillmentInput{OrderID: orderID, Action: biz.OrderActionComplete, IdempotencyKey: "complete-001", Reason: "received"}
	_, err := repo.ApplyFulfillment(f.ctx, owner, ship)
	require.Equal(t, int32(403), kerrors.FromError(err).Code)
	_, err = repo.ApplyFulfillment(f.ctx, owner, complete)
	require.ErrorIs(t, err, biz.ErrOrderFulfillmentConflict)
	_, err = repo.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
	require.NoError(t, err)

	concurrent := func(a biz.Actor, input biz.OrderFulfillmentInput) int64 {
		t.Helper()
		type response struct {
			action *biz.OrderFulfillmentAction
			err    error
		}
		results := make(chan response, 8)
		start := make(chan struct{})
		for range 8 {
			go func() {
				<-start
				action, err := repo.ApplyFulfillment(f.ctx, a, input)
				results <- response{action, err}
			}()
		}
		close(start)
		var id int64
		for range 8 {
			res := <-results
			require.NoError(t, res.err)
			if id == 0 {
				id = res.action.ID
			}
			require.Equal(t, id, res.action.ID)
		}
		return id
	}
	shipmentID := concurrent(admin, ship)
	page, err := repo.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, biz.OrderStatusShipped, page[0].Status)
	other := biz.Actor{ID: f.userID + 100000}
	_, err = repo.ApplyFulfillment(f.ctx, other, complete)
	require.ErrorIs(t, err, biz.ErrOrderNotFound)
	_, err = repo.ListFulfillmentActions(f.ctx, other, orderID)
	require.ErrorIs(t, err, biz.ErrOrderNotFound)
	changed := ship
	changed.TrackingNumber = "different"
	_, err = repo.ApplyFulfillment(f.ctx, admin, changed)
	require.ErrorIs(t, err, biz.ErrIdempotencyKeyConflict)
	changed = ship
	changed.IdempotencyKey = "different-key"
	_, err = repo.ApplyFulfillment(f.ctx, admin, changed)
	require.ErrorIs(t, err, biz.ErrOrderFulfillmentConflict)
	_, _, err = NewPaymentRepo(f.data, f.tx, log.DefaultLogger).PreparePaymentRefund(f.ctx, paymentID, "forbidden-refund")
	require.ErrorIs(t, err, biz.ErrPaymentStateConflict)
	concurrent(owner, complete)
	page, err = repo.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, page)
	order, err := repo.GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, biz.OrderStatusCompleted, order.Status)
	require.True(t, order.IsCompleted)
	replay, err := repo.ApplyFulfillment(f.ctx, admin, ship)
	require.NoError(t, err)
	require.Equal(t, shipmentID, replay.ID, "late replay returns original dispatch receipt")
	audit, err := repo.ListFulfillmentActions(f.ctx, owner, orderID)
	require.NoError(t, err)
	require.Len(t, audit, 2)
	require.Equal(t, ship.TrackingNumber, audit[0].TrackingNumber)
	require.Equal(t, ship.Reason, audit[0].Reason)
	require.Equal(t, admin.ID, audit[0].ActorID)
	require.Equal(t, biz.OrderStatusPaid, audit[0].FromStatus)
	require.Equal(t, biz.OrderStatusShipped, audit[1].FromStatus)
	require.Equal(t, complete.Reason, audit[1].Reason)
}

type failFulfillmentAudit struct{ db.Querier }

func (q failFulfillmentAudit) CreateOrderFulfillmentAction(context.Context, db.CreateOrderFulfillmentActionParams) (db.OrderFulfillmentAction, error) {
	return db.OrderFulfillmentAction{}, errors.New("audit storage unavailable")
}

type failFulfillmentTx struct{ biz.TxManager }

func (tx failFulfillmentTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		return fn(WithQuerier(ctx, failFulfillmentAudit{querierFromContext(ctx, nil)}, pgTxFromContext(ctx)))
	})
}

func TestReviewFulfillmentAuditFailureRollsBackIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, _ := fulfillmentOrder(t, f)
	repo := NewOrderRepo(f.data, failFulfillmentTx{f.tx}, log.DefaultLogger)
	key := redisKey("order", "user", f.userID, "gen")
	require.NoError(t, f.rdb.Set(f.ctx, key, 7, 0).Err())
	_, err := repo.ApplyFulfillment(f.ctx, biz.Actor{ID: f.userID, Admin: true}, shipmentInput(orderID))
	require.ErrorContains(t, err, "audit storage unavailable")
	row, err := f.data.DB(f.ctx).GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, biz.OrderStatusPaid, row.Status)
	require.Equal(t, "7", f.rdb.Get(f.ctx, key).Val())
	rows, err := f.data.DB(f.ctx).ListOrderFulfillmentActions(f.ctx, orderID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestReviewFulfillmentRacesRefundIntegration(t *testing.T) {
	for n := range 6 {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newCorrectnessFixture(t)
			orderID, paymentID := fulfillmentOrder(t, f)
			orders := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
			payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			start := make(chan struct{})
			shipped, refunded := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				_, err := orders.ApplyFulfillment(f.ctx, biz.Actor{ID: f.userID, Admin: true}, shipmentInput(orderID))
				shipped <- err
			}()
			go func() {
				<-start
				_, refund, err := payments.PreparePaymentRefund(f.ctx, paymentID, "race-refund")
				if err == nil {
					err = payments.ApplyPaymentRefund(f.ctx, paymentID, refund.ID)
				}
				refunded <- err
			}()
			close(start)
			shipErr, refundErr := <-shipped, <-refunded
			row, err := f.data.DB(f.ctx).GetOrder(f.ctx, orderID)
			require.NoError(t, err)
			var stock int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
			if shipErr == nil {
				require.ErrorIs(t, refundErr, biz.ErrPaymentStateConflict)
				require.Equal(t, biz.OrderStatusShipped, row.Status)
				require.Equal(t, 99, stock)
			} else {
				require.ErrorIs(t, shipErr, biz.ErrOrderFulfillmentConflict)
				require.NoError(t, refundErr)
				require.Equal(t, biz.OrderStatusRefunded, row.Status)
				require.Equal(t, 100, stock)
			}
		})
	}
}
