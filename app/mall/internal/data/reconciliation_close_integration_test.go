//go:build integration

package data

import (
	"encoding/json"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	malljob "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/job"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

func TestReconciliationRetryAbsentAlipayCloseIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusClosePending)
	_, err := f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, orderID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_minor,product_name_snapshot) VALUES($1,$2,1,12345,'snapshot')`, orderID, f.productID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=99 WHERE id=$1`, f.productID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE payments SET pay_channel='alipay:app',prepay_attempts=1,action_type='invoke',action_payload=jsonb_build_object('payload','signed','provider_account','account_1','channel_expires_at',clock_timestamp()-interval '1 hour') WHERE id=$1`, paymentID)
	require.NoError(t, err)
	mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
	payments := NewPaymentRepoWithJobs(f.data, f.tx, mq, log.DefaultLogger)
	require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "alipay", Reason: "job_exhausted", LastError: "provider unavailable during close"}))
	repo := NewPaymentReconciliationRepo(f.data, f.tx, mq, log.DefaultLogger)
	admin := biz.Actor{ID: f.userID, Admin: true}
	input := reconciliationInput(paymentID, 1, biz.ReconciliationActionRetry, "retry-absent-close")
	action, err := repo.ApplyReconciliation(f.ctx, admin, input)
	require.NoError(t, err)
	replay, err := repo.ApplyReconciliation(f.ctx, admin, input)
	require.NoError(t, err)
	require.Equal(t, action.ID, replay.ID)
	queued, err := mq.GetMQJob(f.ctx, action.JobID)
	require.NoError(t, err)
	require.Equal(t, biz.ClosePayJobKind, queued.Kind)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind IN ('close_pay','check_pay') AND (args->>'payment_id')::bigint=$1`, paymentID).Scan(&count))
	require.Equal(t, 1, count)
	var args biz.ClosePayArgs
	require.NoError(t, json.Unmarshal([]byte(queued.ArgsJSON), &args))
	require.Equal(t, "manual_reconciliation", args.Reason)
	payment, err := payments.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	worker := malljob.NewClosePayWorker(&integrationCloseGateway{payment: payment, absent: true}, payments)
	job := &river.Job[biz.ClosePayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: args}
	for range 2 {
		require.NoError(t, worker.Work(f.ctx, job))
	}
	closed, err := f.data.q.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	require.Equal(t, biz.PaymentStatusClosed, closed.Status)
	require.Equal(t, biz.ReconciliationStatusProcessing, closed.ReconciliationStatus)
	// Channel closure does not bypass human review. Resolve, then let normal
	// expiry release stock; repeated settlement/expiry must remain idempotent.
	_, err = repo.ApplyReconciliation(f.ctx, admin, reconciliationInput(paymentID, 2, biz.ReconciliationActionResolve, "resolve-absent-close"))
	require.NoError(t, err)
	expiry := NewOrderExpiryRepo(f.data, f.tx, mq, log.DefaultLogger)
	for range 2 {
		require.NoError(t, expiry.ExpireOrder(f.ctx, orderID))
	}
	order, err := f.data.q.GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, biz.OrderStatusCancelled, order.Status)
	var stock int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 100, stock)
}
