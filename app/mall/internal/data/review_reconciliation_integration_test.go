//go:build integration

package data

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func reconciliationInput(id, version int64, action, key string) biz.ReconciliationInput {
	return biz.ReconciliationInput{PaymentID: id, ExpectedVersion: version, Action: action, IdempotencyKey: key, Reason: "verified with provider", Evidence: "sandbox statement reference"}
}

func TestReviewReconciliationRetryResolveAndReopenIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, trade := f.seedPayment(t, biz.PaymentStatusPending)
	payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "job_exhausted", LastError: "provider temporarily unavailable"}))
	mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
	repo := NewPaymentReconciliationRepo(f.data, f.tx, mq, log.DefaultLogger)
	admin := biz.Actor{ID: f.userID, Admin: true}
	input := reconciliationInput(paymentID, 1, biz.ReconciliationActionRetry, "manual-retry-001")
	_, err := repo.ApplyReconciliation(f.ctx, biz.Actor{ID: f.userID}, input)
	require.Error(t, err)
	type result struct {
		action *biz.ReconciliationAction
		err    error
	}
	results := make(chan result, 8)
	for range 8 {
		go func() { action, err := repo.ApplyReconciliation(f.ctx, admin, input); results <- result{action, err} }()
	}
	var first *biz.ReconciliationAction
	for range 8 {
		res := <-results
		require.NoError(t, res.err)
		if first == nil {
			first = res.action
		}
		require.Equal(t, first.ID, res.action.ID)
	}
	require.Equal(t, biz.ReconciliationStatusProcessing, first.ToStatus)
	require.EqualValues(t, 2, first.ToVersion)
	require.Positive(t, first.JobID)
	job, err := mq.GetMQJob(f.ctx, first.JobID)
	require.NoError(t, err)
	require.Contains(t, job.ArgsJSON, "manual_reconciliation")
	var jobs int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'payment_id')::bigint=$2`, biz.CheckPayJobKind, paymentID).Scan(&jobs))
	require.Equal(t, 1, jobs)
	changed := input
	changed.Reason = "another intent"
	_, err = repo.ApplyReconciliation(f.ctx, admin, changed)
	require.ErrorIs(t, err, biz.ErrIdempotencyKeyConflict)
	_, err = repo.ApplyReconciliation(f.ctx, admin, reconciliationInput(paymentID, 2, biz.ReconciliationActionResolve, "resolve-unsettled"))
	require.ErrorIs(t, err, biz.ErrReconciliationConflict)
	// Existing query settlement remains the only writer of financial facts.
	err = payments.ApplyPayQuery(f.ctx, biz.CheckPayArgs{PaymentID: paymentID, Provider: "wechat"}, &biz.PaymentQueryResult{Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: trade, TransactionID: fmt.Sprintf("verified-%d", paymentID), Amount: 12345, Currency: "CNY", TradeState: biz.TradeStateSuccess})
	require.NoError(t, err)
	resolve := reconciliationInput(paymentID, 2, biz.ReconciliationActionResolve, "manual-resolve-001")
	receipt, err := repo.ApplyReconciliation(f.ctx, admin, resolve)
	require.NoError(t, err)
	require.Equal(t, biz.ReconciliationStatusResolved, receipt.ToStatus)
	require.EqualValues(t, 3, receipt.ToVersion)
	replay, err := repo.ApplyReconciliation(f.ctx, admin, resolve)
	require.NoError(t, err)
	require.Equal(t, receipt.ID, replay.ID)
	var stock int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 100, stock, "resolution never edits inventory")
	var orderStatus string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, orderID).Scan(&orderStatus))
	require.Equal(t, biz.OrderStatusPaid, orderStatus)
	actions, err := repo.ListReconciliationActions(f.ctx, paymentID, 0, 100)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	require.Equal(t, admin.ID, actions[0].ActorID)
	// A later independently verified anomaly must reopen the case. An old
	// resolution cannot acknowledge it, even though the financial state is paid.
	require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "provider_mismatch", LastError: "new provider transaction mismatch"}))
	resolve.ExpectedVersion = 3
	resolve.IdempotencyKey = "stale-resolve-001"
	_, err = repo.ApplyReconciliation(f.ctx, admin, resolve)
	require.ErrorIs(t, err, biz.ErrReconciliationConflict)
	cases, err := repo.ListReconciliationCases(f.ctx, 0, 100)
	require.NoError(t, err)
	require.Len(t, cases, 1)
	require.EqualValues(t, 4, cases[0].Payment.ReconciliationVersion)
	require.Equal(t, biz.ReconciliationStatusRequired, cases[0].Payment.ReconciliationStatus)
}

type reconciliationAuditFailure struct{ db.Querier }

func (reconciliationAuditFailure) CreateReconciliationAction(context.Context, db.CreateReconciliationActionParams) (db.PaymentReconciliationAction, error) {
	return db.PaymentReconciliationAction{}, errors.New("injected audit failure")
}

type reconciliationFaultTx struct{ biz.TxManager }

func (t reconciliationFaultTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return t.TxManager.InTx(ctx, func(ctx context.Context) error {
		return fn(WithQuerier(ctx, reconciliationAuditFailure{querierFromContext(ctx, nil)}, nil))
	})
}

type reconciliationFailedJobs struct{ biz.PaymentMQRepo }

func (reconciliationFailedJobs) EnqueueCheckPayTx(context.Context, biz.CheckPayArgs, time.Time) (*biz.MQJob, error) {
	return nil, errors.New("injected queue failure")
}

func TestReviewReconciliationQueueAuditFailuresRollbackIntegration(t *testing.T) {
	for _, fault := range []string{"queue", "audit"} {
		t.Run(fault, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			_, paymentID, _ := f.seedPayment(t, biz.PaymentStatusPending)
			payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "job_exhausted", LastError: "timeout"}))
			tx, jobs := f.tx, biz.PaymentMQRepo(NewPaymentMQRepo(f.riverClient, log.DefaultLogger))
			if fault == "audit" {
				tx = reconciliationFaultTx{f.tx}
			} else {
				jobs = reconciliationFailedJobs{}
			}
			_, err := NewPaymentReconciliationRepo(f.data, tx, jobs, log.DefaultLogger).ApplyReconciliation(f.ctx, biz.Actor{ID: f.userID, Admin: true}, reconciliationInput(paymentID, 1, biz.ReconciliationActionRetry, "retry-fault-001"))
			require.Error(t, err)
			row, err := f.data.q.GetPayment(f.ctx, paymentID)
			require.NoError(t, err)
			require.Equal(t, biz.ReconciliationStatusRequired, row.ReconciliationStatus)
			require.EqualValues(t, 1, row.ReconciliationVersion)
			var count int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'payment_id')::bigint=$2`, biz.CheckPayJobKind, paymentID).Scan(&count))
			require.Zero(t, count, "retry and its audit commit together with the job")
		})
	}
}

func TestReviewReconciliationCompensationRequiredIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, trade := f.seedPayment(t, biz.PaymentStatusSuccess)
	var primaryID int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `INSERT INTO payments(order_id,user_id,merchant_id,amount_minor,status,pay_channel,out_trade_no,third_party_tx_id,currency) VALUES ($1,$2,0,12345,'success','wechat:native',$3,$4,'CNY') RETURNING id`, orderID, f.userID, trade+"-primary", trade+"-primary-tx").Scan(&primaryID))
	_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status='paid',paid_payment_id=$2 WHERE id=$1`, orderID, primaryID)
	require.NoError(t, err)
	payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "duplicate_success", LastError: "duplicate money"}))
	repo := NewPaymentReconciliationRepo(f.data, f.tx, nil, log.DefaultLogger)
	input := reconciliationInput(paymentID, 1, biz.ReconciliationActionResolve, "resolve-duplicate")
	_, err = repo.ApplyReconciliation(f.ctx, biz.Actor{ID: f.userID, Admin: true}, input)
	require.ErrorIs(t, err, biz.ErrReconciliationConflict)
	_, refund, err := payments.PreparePaymentRefund(f.ctx, paymentID, trade+"-refund")
	require.NoError(t, err)
	require.Equal(t, biz.RefundDuplicate, refund.Purpose)
	require.NoError(t, payments.ApplyPaymentRefund(f.ctx, paymentID, refund.ID))
	receipt, err := repo.ApplyReconciliation(f.ctx, biz.Actor{ID: f.userID, Admin: true}, input)
	require.NoError(t, err)
	require.Equal(t, biz.ReconciliationStatusResolved, receipt.ToStatus)
	primary, err := f.data.q.GetPayment(f.ctx, primaryID)
	require.NoError(t, err)
	require.Equal(t, biz.PaymentStatusSuccess, primary.Status)
	order, err := f.data.q.GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, pgtype.Int8{Int64: primaryID, Valid: true}, order.PaidPaymentID)
}
