//go:build integration

package data

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	malljob "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/job"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type barrierSigningGateway struct {
	biz.PaymentGateway
	started, release chan struct{}
	fail             bool
}

func (g *barrierSigningGateway) Capabilities(biz.PaymentMethod) (biz.PaymentCapabilities, error) {
	return biz.PaymentCapabilities{SupportsClose: true}, nil
}
func (g *barrierSigningGateway) Query(context.Context, biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	return nil, biz.ErrProviderOrderNotExist
}
func (g *barrierSigningGateway) Prepay(ctx context.Context, _ biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	close(g.started) // ClaimPaymentPrepay has committed, but finalize has not run.
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if g.fail {
		return nil, errors.New("local signing failed")
	}
	return &biz.PaymentPrepayResult{Action: biz.PaymentAction{Type: "invoke", Payload: []byte(`{"payload":"signed-but-never-issued"}`)}}, nil
}

func TestPrepayClaimExpirySignerBarrierIntegration(t *testing.T) {
	for _, product := range []string{"app", "wap"} {
		for _, mode := range []string{"signer-success", "signer-failure", "lease-expired-before-signer"} {
			t.Run(product+"/"+mode, func(t *testing.T) {
				f := newCorrectnessFixture(t)
				orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusCreating)
				_, err := f.pool.Exec(f.ctx, `UPDATE payments SET pay_channel=$2 WHERE id=$1`, paymentID, "alipay:"+product)
				require.NoError(t, err)
				_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items (order_id, product_id, quantity, unit_price_minor, product_name_snapshot) VALUES ($1,$2,1,12345,'snapshot')`, orderID, f.productID)
				require.NoError(t, err)
				_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=99 WHERE id=$1`, f.productID)
				require.NoError(t, err)
				var orderNo string
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT out_trade_no FROM orders WHERE id=$1`, orderID).Scan(&orderNo))
				repo := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
				orders := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
				gateway := &barrierSigningGateway{started: make(chan struct{}), release: make(chan struct{}), fail: mode == "signer-failure"}
				uc := biz.NewConfiguredPaymentUsecase(gateway, repo, nil, orders, nil, f.tx, shippingIDGenerator{f.prefix}, biz.PaymentPolicy{PrepayLeaseDuration: time.Hour}, log.DefaultLogger)
				ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
				var wg sync.WaitGroup
				defer func() { cancel(); wg.Wait() }()
				type outcome struct {
					result *biz.PrepayForOrderResult
					err    error
				}
				done := make(chan outcome, 1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := uc.PrepayForOrder(ctx, biz.PrepayForOrderArgs{UserID: f.userID, OrderNo: orderNo, Method: biz.PaymentMethod{Provider: "alipay", Product: product}})
					done <- outcome{result, err}
				}()
				select {
				case <-gateway.started:
				case result := <-done:
					t.Fatalf("prepay stopped before barrier: %v", result.err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				claimed, err := repo.GetPaymentForJob(ctx, paymentID)
				require.NoError(t, err)
				require.NotEmpty(t, claimed.PrepayLeaseToken)
				require.NotNil(t, claimed.PrepayLeaseUntil)
				_, err = f.pool.Exec(ctx, `UPDATE orders SET expires_at=now()-interval '1 second' WHERE id=$1`, orderID)
				require.NoError(t, err)
				expiry := NewOrderExpiryRepo(f.data, f.tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
				require.NoError(t, expiry.ExpireOrder(ctx, orderID))
				closing, err := repo.GetPaymentForJob(ctx, paymentID)
				require.NoError(t, err)
				require.Equal(t, biz.PaymentStatusClosePending, closing.Status)
				// Wrong-token cleanup cannot clear the in-flight signer's lease.
				require.NoError(t, repo.RecordPaymentPrepayError(ctx, paymentID, "wrong-token", "late failure"))
				closing, err = repo.GetPaymentForJob(ctx, paymentID)
				require.NoError(t, err)
				require.Equal(t, claimed.PrepayLeaseToken, closing.PrepayLeaseToken)
				worker := malljob.NewClosePayWorker(gateway, repo)
				job := &river.Job[biz.ClosePayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.ClosePayArgs{PaymentID: paymentID, Provider: "alipay"}}
				var snooze *river.JobSnoozeError
				require.ErrorAs(t, worker.Work(ctx, job), &snooze)
				if mode == "lease-expired-before-signer" {
					// Simulate a signer/process that never cleaned its lease, without sleeps.
					_, err = f.pool.Exec(ctx, `UPDATE payments SET prepay_lease_until=now()-interval '1 second' WHERE id=$1`, paymentID)
					require.NoError(t, err)
					require.NoError(t, worker.Work(ctx, job))
				}
				close(gateway.release)
				select {
				case result := <-done:
					require.Error(t, result.err)
					require.Nil(t, result.result, "late finalize must never expose signed payment parameters")
					if !gateway.fail {
						require.ErrorIs(t, result.err, biz.ErrPaymentStateConflict)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				after, err := repo.GetPaymentForJob(ctx, paymentID)
				require.NoError(t, err)
				require.Empty(t, after.PrepayLeaseToken)
				require.Nil(t, after.PrepayLeaseUntil)
				require.Empty(t, after.Action.Type)
				require.Empty(t, after.Action.Payload)
				require.NoError(t, worker.Work(ctx, job))
				require.NoError(t, worker.Work(ctx, job))
				late, err := repo.FinalizePaymentPrepay(ctx, paymentID, claimed.PrepayLeaseToken, biz.PaymentAction{Type: "invoke", Payload: []byte(`{}`)})
				require.ErrorIs(t, err, biz.ErrPaymentStateConflict)
				require.Nil(t, late)
				var orderStatus, paymentStatus, reconciliation string
				var stock, failures int64
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, orderID).Scan(&orderStatus))
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT status,reconciliation_status FROM payments WHERE id=$1`, paymentID).Scan(&paymentStatus, &reconciliation))
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM payment_reconciliation_failures WHERE payment_id=$1`, paymentID).Scan(&failures))
				require.Equal(t, biz.OrderStatusCancelled, orderStatus)
				require.Equal(t, biz.PaymentStatusClosed, paymentStatus)
				require.Equal(t, biz.ReconciliationStatusNone, reconciliation)
				require.EqualValues(t, 100, stock)
				require.Zero(t, failures)
			})
		}
	}
}
