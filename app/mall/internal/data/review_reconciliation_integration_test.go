//go:build integration

package data

import (
	"context"
	"encoding/json"
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
	var unresolved int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM payment_reconciliation_failures WHERE payment_id=$1 AND resolved_at IS NULL`, paymentID).Scan(&unresolved))
	require.Zero(t, unresolved, "resolution closes the persisted failure records in the same transaction")
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
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM payment_reconciliation_failures WHERE payment_id=$1 AND resolved_at IS NULL`, paymentID).Scan(&unresolved))
	require.Equal(t, 1, unresolved, "new anomalies retain their own unresolved evidence")
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

func (reconciliationFailedJobs) EnqueueClosePayTx(context.Context, biz.ClosePayArgs, time.Time) (*biz.MQJob, error) {
	return nil, errors.New("injected queue failure")
}

func TestReviewReconciliationQueueAuditFailuresRollbackIntegration(t *testing.T) {
	for _, tc := range []struct{ fault, status, kind string }{
		{"queue", biz.PaymentStatusPending, biz.CheckPayJobKind},
		{"audit", biz.PaymentStatusPending, biz.CheckPayJobKind},
		{"queue", biz.PaymentStatusClosePending, biz.ClosePayJobKind},
		{"audit", biz.PaymentStatusClosePending, biz.ClosePayJobKind},
	} {
		t.Run(tc.status+"/"+tc.fault, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			_, paymentID, _ := f.seedPayment(t, tc.status)
			payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "job_exhausted", LastError: "timeout"}))
			tx, jobs := f.tx, biz.PaymentMQRepo(NewPaymentMQRepo(f.riverClient, log.DefaultLogger))
			if tc.fault == "audit" {
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
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'payment_id')::bigint=$2`, tc.kind, paymentID).Scan(&count))
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

func TestReviewReconciliationProcessingBlocksOrderChangesUntilResolvedIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, trade := f.seedPayment(t, biz.PaymentStatusPending)
	_, err := f.pool.Exec(f.ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_minor,product_name_snapshot,cover_image_snapshot) VALUES($1,$2,1,12345,'product','[]')`, orderID, f.productID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=stock-1 WHERE id=$1`, f.productID)
	require.NoError(t, err)
	// Begin in a valid payment window, then let it expire during manual review.
	_, err = f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, orderID)
	require.NoError(t, err)
	mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
	payments := NewPaymentRepoWithJobs(f.data, f.tx, mq, log.DefaultLogger)
	orders := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
	expiry := NewOrderExpiryRepo(f.data, f.tx, mq, log.DefaultLogger)
	repo := NewPaymentReconciliationRepo(f.data, f.tx, mq, log.DefaultLogger)
	admin := biz.Actor{ID: f.userID, Admin: true}
	require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: "job_exhausted", LastError: "timeout"}))
	_, err = repo.ApplyReconciliation(f.ctx, admin, reconciliationInput(paymentID, 1, biz.ReconciliationActionRetry, "processing-retry"))
	require.NoError(t, err)
	_, err = payments.CreatePayment(f.ctx, biz.CreatePaymentArgs{OrderID: orderID, UserID: f.userID, Amount: 12345, Currency: "CNY", Method: "wechat:native", OutTradeNo: trade + "-new"})
	require.ErrorIs(t, err, biz.ErrPaymentReconciliationRequired)
	require.ErrorIs(t, orders.CancelOrderByUser(f.ctx, orderID, f.userID), biz.ErrPaymentReconciliationRequired)
	_, err = f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, orderID)
	require.NoError(t, err)
	require.ErrorIs(t, expiry.ExpireOrder(f.ctx, orderID), biz.ErrPaymentReconciliationRequired)
	require.NoError(t, payments.ApplyPayQuery(f.ctx, biz.CheckPayArgs{PaymentID: paymentID, Provider: "wechat", Trigger: "manual_reconciliation"}, &biz.PaymentQueryResult{Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: trade, TradeState: biz.TradeStateClosed}))
	order, err := f.data.q.GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, biz.OrderStatusPendingPayment, order.Status, "query settlement respects unresolved review")
	resolve := reconciliationInput(paymentID, 2, biz.ReconciliationActionResolve, "resolve-closed-order")
	_, err = NewPaymentReconciliationRepo(f.data, reconciliationFaultTx{f.tx}, mq, log.DefaultLogger).ApplyReconciliation(f.ctx, admin, resolve)
	require.ErrorContains(t, err, "audit failure")
	payment, err := f.data.q.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	require.Equal(t, biz.ReconciliationStatusProcessing, payment.ReconciliationStatus)
	var unresolved int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM payment_reconciliation_failures WHERE payment_id=$1 AND resolved_at IS NULL`, paymentID).Scan(&unresolved))
	require.Equal(t, 1, unresolved, "failed audit rolls back failure closure too")
	_, err = repo.ApplyReconciliation(f.ctx, admin, resolve)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, expiry.ExpireOrder(f.ctx, orderID))
	}
	order, err = f.data.q.GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, biz.OrderStatusCancelled, order.Status)
	var stock int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 100, stock, "expiry releases stock exactly once after review")
}

func TestReviewReconciliationSiblingAfterPrimaryRefundIntegration(t *testing.T) {
	for _, status := range []string{biz.PaymentStatusClosed, biz.PaymentStatusFailed, biz.PaymentStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			orderID, primaryID, trade := f.seedPayment(t, biz.PaymentStatusSuccess)
			_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status='paid',paid_payment_id=$2 WHERE id=$1`, orderID, primaryID)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `UPDATE payments SET third_party_tx_id=$2 WHERE id=$1`, primaryID, trade+"-tx")
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_minor,product_name_snapshot,cover_image_snapshot) VALUES($1,$2,1,12345,'product','[]')`, orderID, f.productID)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=stock-1 WHERE id=$1`, f.productID)
			require.NoError(t, err)
			initialStatus := status
			if status == biz.PaymentStatusRefunded {
				initialStatus = biz.PaymentStatusSuccess
			}
			var siblingID int64
			require.NoError(t, f.pool.QueryRow(f.ctx, `INSERT INTO payments(order_id,user_id,merchant_id,amount_minor,status,pay_channel,out_trade_no,third_party_tx_id,currency) VALUES($1,$2,0,12345,$3,'wechat:native',$4,$5,'CNY') RETURNING id`, orderID, f.userID, initialStatus, trade+"-sibling", trade+"-sibling-tx").Scan(&siblingID))
			payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			if status == biz.PaymentStatusRefunded {
				_, refund, err := payments.PreparePaymentRefund(f.ctx, siblingID, trade+"-sibling-refund")
				require.NoError(t, err)
				require.NoError(t, payments.ApplyPaymentRefund(f.ctx, siblingID, refund.ID))
			}
			require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: siblingID, Provider: "wechat", Reason: "job_exhausted", LastError: "timeout"}))
			repo := NewPaymentReconciliationRepo(f.data, f.tx, nil, log.DefaultLogger)
			admin := biz.Actor{ID: f.userID, Admin: true}
			input := reconciliationInput(siblingID, 1, biz.ReconciliationActionResolve, "resolve-settled-sibling")
			_, primaryRefund, err := payments.PreparePaymentRefund(f.ctx, primaryID, trade+"-primary-refund")
			require.NoError(t, err)
			_, err = repo.ApplyReconciliation(f.ctx, admin, input)
			require.ErrorIs(t, err, biz.ErrReconciliationConflict, "pending primary refund must not be acknowledged")
			require.NoError(t, payments.ApplyPaymentRefund(f.ctx, primaryID, primaryRefund.ID))
			// A refunded status alone cannot replace a matching settlement receipt.
			_, err = f.pool.Exec(f.ctx, `UPDATE order_refunds SET refund_amount_minor=12344 WHERE id=$1`, primaryRefund.ID)
			require.NoError(t, err)
			_, err = repo.ApplyReconciliation(f.ctx, admin, input)
			require.ErrorIs(t, err, biz.ErrReconciliationConflict)
			_, err = f.pool.Exec(f.ctx, `UPDATE order_refunds SET refund_amount_minor=12345 WHERE id=$1`, primaryRefund.ID)
			require.NoError(t, err)
			for range 2 {
				receipt, err := repo.ApplyReconciliation(f.ctx, admin, input)
				require.NoError(t, err)
				require.Equal(t, biz.ReconciliationStatusResolved, receipt.ToStatus)
			}
			var stock int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
			require.Equal(t, 100, stock, "only the primary refund restores stock; resolution and replay do not")
			actions, err := repo.ListReconciliationActions(f.ctx, siblingID, 0, 100)
			require.NoError(t, err)
			require.Len(t, actions, 1)
		})
	}
}

func TestReconciliationRetryDoesNotReuseDelayedJobIntegration(t *testing.T) {
	for _, status := range []string{biz.PaymentStatusPending, biz.PaymentStatusClosePending} {
		t.Run(status, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			_, paymentID, _ := f.seedPayment(t, status)
			mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
			var old *biz.MQJob
			var err error
			if status == biz.PaymentStatusClosePending {
				old, err = mq.EnqueueClosePay(f.ctx, biz.ClosePayArgs{PaymentID: paymentID, Provider: "wechat", Reason: "expired"}, time.Now().Add(time.Hour))
			} else {
				old, err = mq.EnqueueCheckPay(f.ctx, biz.CheckPayArgs{PaymentID: paymentID, Provider: "wechat", Trigger: "prepay", MaxPolls: 100}, time.Now().Add(time.Hour))
			}
			require.NoError(t, err)
			payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			repo := NewPaymentReconciliationRepo(f.data, f.tx, mq, log.DefaultLogger)
			admin := biz.Actor{ID: f.userID, Admin: true}
			previousID := old.ID
			// A reopened case must also get a fresh immediate job, even while
			// the preceding manual retry is still available/running.
			for attempt := int64(1); attempt <= 2; attempt++ {
				require.NoError(t, payments.MarkReconciliationRequired(f.ctx, biz.ReconciliationFailure{PaymentID: paymentID, Provider: "wechat", Reason: fmt.Sprintf("failure_%d", attempt), LastError: "timeout"}))
				input := reconciliationInput(paymentID, attempt*2-1, biz.ReconciliationActionRetry, fmt.Sprintf("immediate-retry-%d", attempt))
				type result struct {
					action *biz.ReconciliationAction
					err    error
				}
				results := make(chan result, 4)
				for range 4 {
					go func() {
						action, err := repo.ApplyReconciliation(f.ctx, admin, input)
						results <- result{action, err}
					}()
				}
				var action *biz.ReconciliationAction
				for range 4 {
					r := <-results
					require.NoError(t, r.err)
					if action != nil {
						require.Equal(t, action.ID, r.action.ID)
						require.Equal(t, action.JobID, r.action.JobID)
					}
					action = r.action
				}
				require.NotEqual(t, old.ID, action.JobID)
				require.NotEqual(t, previousID, action.JobID)
				previousID = action.JobID
				job, err := mq.GetMQJob(f.ctx, action.JobID)
				require.NoError(t, err)
				require.Equal(t, "available", job.State)
				require.False(t, job.ScheduledAt.After(time.Now()), "manual retry must be eligible immediately")
				if status == biz.PaymentStatusClosePending {
					var args biz.ClosePayArgs
					require.NoError(t, json.Unmarshal([]byte(job.ArgsJSON), &args))
					require.Equal(t, "manual_reconciliation", args.Reason)
					require.Equal(t, action.ToVersion, args.ReconciliationVersion)
				} else {
					var args biz.CheckPayArgs
					require.NoError(t, json.Unmarshal([]byte(job.ArgsJSON), &args))
					require.Equal(t, "manual_reconciliation", args.Trigger)
					require.Equal(t, action.ToVersion, args.ReconciliationVersion)
					require.Equal(t, 5, args.MaxPolls)
					require.Equal(t, 30, args.PollIntervalSeconds)
				}
				var count int
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind=$1 AND (args->>'payment_id')::bigint=$2`, job.Kind, paymentID).Scan(&count))
				require.EqualValues(t, attempt+1, count, "same-key requests enqueue only once")
			}
			unchanged, err := mq.GetMQJob(f.ctx, old.ID)
			require.NoError(t, err)
			require.Equal(t, old.ScheduledAt, unchanged.ScheduledAt)
			require.Equal(t, old.ArgsJSON, unchanged.ArgsJSON)
		})
	}
}
