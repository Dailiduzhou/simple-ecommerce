package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type PaymentReconciliationRepo struct {
	data *Data
	tx   biz.TxManager
	jobs biz.PaymentMQRepo
	log  *log.Helper
}

func NewPaymentReconciliationRepo(data *Data, tx biz.TxManager, jobs biz.PaymentMQRepo, logger log.Logger) *PaymentReconciliationRepo {
	return &PaymentReconciliationRepo{data: data, tx: tx, jobs: jobs, log: log.NewHelper(logger)}
}

var _ biz.PaymentReconciliationRepo = (*PaymentReconciliationRepo)(nil)

func (r *PaymentReconciliationRepo) ApplyReconciliation(ctx context.Context, actor biz.Actor, input biz.ReconciliationInput) (*biz.ReconciliationAction, error) {
	if err := input.Validate(actor); err != nil {
		return nil, err
	}
	var action db.PaymentReconciliationAction
	var changed db.Payment
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		// Resolve the parent without cached state, then follow the common order /
		// ascending payment locks used by callbacks, expiry and refund settlement.
		snapshot, err := q.GetPayment(ctx, input.PaymentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.ErrPaymentNotFound
		}
		if err != nil {
			return err
		}
		order, err := q.GetOrderForUpdate(ctx, snapshot.OrderID)
		if err != nil {
			return err
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, order.ID)
		if err != nil {
			return err
		}
		var payment db.Payment
		for _, row := range payments {
			if row.ID == input.PaymentID {
				payment = row
				break
			}
		}
		if payment.ID == 0 {
			return biz.ErrPaymentNotFound
		}
		action, err = q.GetReconciliationAction(ctx, db.GetReconciliationActionParams{PaymentID: payment.ID, IdempotencyKey: input.IdempotencyKey})
		if err == nil {
			if action.ActorID != actor.ID || action.Action != input.Action || action.FromVersion != input.ExpectedVersion || action.Reason != input.Reason || action.Evidence != input.Evidence {
				return biz.ErrIdempotencyKeyConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if payment.ReconciliationVersion != input.ExpectedVersion {
			return biz.ErrReconciliationConflict
		}
		var jobID pgtype.Int8
		if input.Action == biz.ReconciliationActionRetry {
			if payment.ReconciliationStatus != biz.ReconciliationStatusRequired {
				return biz.ErrReconciliationConflict
			}
			if r.jobs == nil {
				return fmt.Errorf("reconciliation requires payment jobs")
			}
			method, err := biz.ParsePaymentMethod(payment.PayChannel)
			if err != nil {
				return err
			}
			changed, err = q.RetryPaymentReconciliation(ctx, db.RetryPaymentReconciliationParams{ID: payment.ID, ReconciliationVersion: input.ExpectedVersion})
			if err != nil {
				return err
			}
			job, err := r.jobs.EnqueueCheckPayTx(ctx, biz.CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "manual_reconciliation", MaxPolls: 5, PollIntervalSeconds: 30, OrderExpiresAt: order.ExpiresAt.Time}, time.Time{})
			if err != nil {
				return err
			}
			if job == nil || job.ID <= 0 {
				return fmt.Errorf("reconciliation enqueue returned no job")
			}
			jobID = pgtype.Int8{Int64: job.ID, Valid: true}
		} else {
			var refund *biz.PaymentRefund
			row, err := q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: payment.ID, Valid: true})
			if err == nil {
				refund = toBizPaymentRefund(row)
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if refund != nil && refund.Status == biz.PaymentRefundStatusPending {
				return biz.ErrReconciliationConflict
			}
			expired, err := q.OrderIsExpired(ctx, order.ID)
			if err != nil {
				return err
			}
			if !biz.CanResolveReconciliation(toBizPayment(payment), toBizOrder(order), refund, expired) {
				return biz.ErrReconciliationConflict
			}
			// An order funded by a sibling still needs a valid primary payment.
			if order.PaidPaymentID.Valid && order.PaidPaymentID.Int64 != payment.ID {
				validPrimary := false
				for _, candidate := range payments {
					if candidate.ID == order.PaidPaymentID.Int64 && candidate.Status == biz.PaymentStatusSuccess && candidate.UserID == order.UserID && candidate.AmountMinor == order.TotalAmountMinor && candidate.Currency == order.Currency {
						validPrimary = true
					}
				}
				if !validPrimary {
					return biz.ErrReconciliationConflict
				}
			}
			changed, err = q.ResolvePaymentReconciliation(ctx, db.ResolvePaymentReconciliationParams{ID: payment.ID, ReconciliationVersion: input.ExpectedVersion})
			if err != nil {
				return err
			}
		}
		action, err = q.CreateReconciliationAction(ctx, db.CreateReconciliationActionParams{
			PaymentID: payment.ID, ActorID: actor.ID, Action: input.Action, FromStatus: payment.ReconciliationStatus, ToStatus: changed.ReconciliationStatus,
			FromVersion: payment.ReconciliationVersion, ToVersion: changed.ReconciliationVersion, IdempotencyKey: input.IdempotencyKey, Reason: input.Reason, Evidence: input.Evidence, RiverJobID: jobID,
		})
		if err == nil {
			(&PaymentRepo{data: r.data, log: r.log}).invalidatePayment(ctx, changed)
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = biz.ErrReconciliationConflict
	}
	if err != nil {
		observability.PaymentReconciliationAction(ctx, input.Action, "failed")
		return nil, err
	}
	if changed.ID > 0 {
		observability.PaymentReconciliationAction(ctx, input.Action, "committed")
	}
	value := toBizReconciliationAction(action)
	return &value, nil
}

func (r *PaymentReconciliationRepo) ListReconciliationCases(ctx context.Context, afterID int64, limit int32) ([]biz.ReconciliationCase, error) {
	rows, err := r.data.DB(ctx).ListReconciliationCases(ctx, db.ListReconciliationCasesParams{ID: afterID, Limit: limit})
	if err != nil {
		return nil, err
	}
	result := make([]biz.ReconciliationCase, len(rows))
	for i, row := range rows {
		result[i] = biz.ReconciliationCase{Payment: toBizPayment(row), Detail: row.ReconciliationDetail.String}
	}
	return result, nil
}

func (r *PaymentReconciliationRepo) ListReconciliationActions(ctx context.Context, paymentID, afterID int64, limit int32) ([]biz.ReconciliationAction, error) {
	_, err := r.data.DB(ctx).GetPayment(ctx, paymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPaymentNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := r.data.DB(ctx).ListReconciliationActions(ctx, db.ListReconciliationActionsParams{PaymentID: paymentID, ID: afterID, Limit: limit})
	if err != nil {
		return nil, err
	}
	result := make([]biz.ReconciliationAction, len(rows))
	for i, row := range rows {
		result[i] = toBizReconciliationAction(row)
	}
	return result, nil
}

func toBizReconciliationAction(row db.PaymentReconciliationAction) biz.ReconciliationAction {
	return biz.ReconciliationAction{ID: row.ID, PaymentID: row.PaymentID, ActorID: row.ActorID, Action: row.Action, FromStatus: row.FromStatus, ToStatus: row.ToStatus, FromVersion: row.FromVersion, ToVersion: row.ToVersion, IdempotencyKey: row.IdempotencyKey, Reason: row.Reason, Evidence: row.Evidence, JobID: row.RiverJobID.Int64, CreatedAt: row.CreatedAt.Time}
}
