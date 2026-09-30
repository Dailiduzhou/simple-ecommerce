package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type PaymentRiverErrorHandler struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	log  *log.Helper
}

func NewPaymentRiverErrorHandler(pool *pgxpool.Pool, rdb *redis.Client, logger log.Logger) *PaymentRiverErrorHandler {
	return &PaymentRiverErrorHandler{pool: pool, rdb: rdb, log: log.NewHelper(logger)}
}

func (h *PaymentRiverErrorHandler) HandleError(ctx context.Context, job *rivertype.JobRow, workErr error) *river.ErrorHandlerResult {
	if job != nil && job.Attempt >= job.MaxAttempts && (job.Kind == biz.CacheInvalidationJobKind || job.Kind == biz.HistoryCleanupKind || job.Kind == biz.MediaSweepKind || job.Kind == biz.MediaDeleteKind) {
		observability.RiverJobDiscarded(ctx, job.Kind)
		h.log.WithContext(ctx).Errorw("msg", "community job discarded; periodic maintenance will retry", "event", "river_job_discarded", "job_id", job.ID, "kind", job.Kind)
		return nil
	}
	if job != nil && job.Attempt >= job.MaxAttempts && job.Kind == biz.ExpireOrderJobKind {
		// There is no payment state to reconcile here, and the overdue-order
		// reaper re-enqueues expiry within minutes, but the discard must stay
		// observable so persistent queue failures surface in metrics.
		observability.RiverJobDiscarded(ctx, job.Kind)
		h.log.WithContext(ctx).Errorw("msg", "expire order job discarded; the reaper will re-enqueue it", "event", "river_job_discarded", "job_id", job.ID)
		return nil
	}
	if job == nil || job.Attempt < job.MaxAttempts ||
		(job.Kind != biz.CheckPayJobKind && job.Kind != biz.ClosePayJobKind) {
		return nil
	}
	var paymentID, notificationID int64
	var provider string
	switch job.Kind {
	case biz.CheckPayJobKind:
		var args biz.CheckPayArgs
		if err := json.Unmarshal(job.EncodedArgs, &args); err != nil {
			h.log.WithContext(ctx).Errorw("msg", "decode discarded payment job failed", "job_id", job.ID, "error", err)
			return nil
		}
		paymentID, notificationID, provider = args.PaymentID, args.NotificationID, args.Provider
	case biz.ClosePayJobKind:
		var args biz.ClosePayArgs
		if err := json.Unmarshal(job.EncodedArgs, &args); err != nil {
			h.log.WithContext(ctx).Errorw("msg", "decode discarded close job failed", "job_id", job.ID, "error", err)
			return nil
		}
		paymentID, provider = args.PaymentID, args.Provider
	}
	if paymentID <= 0 {
		h.log.WithContext(ctx).Errorw("msg", "discarded payment job has invalid payment_id", "job_id", job.ID)
		return nil
	}
	lastError := "payment reconciliation job exhausted"
	if workErr != nil {
		lastError = workErr.Error()
	}
	var payment db.Payment
	reconcileRequired := false
	tx, err := h.pool.Begin(ctx)
	if err == nil {
		q := db.New(tx)
		if notificationID > 0 {
			var rows int64
			rows, err = q.MarkPaymentNotificationFailed(ctx, db.MarkPaymentNotificationFailedParams{
				ID: notificationID, LastError: notificationErrorText(lastError),
			})
			if err == nil && rows == 0 {
				err = notificationStateAfterCAS(ctx, q, notificationID, biz.PaymentNotificationStatusProcessed)
			}
		}
		if err == nil {
			var snapshot db.Payment
			snapshot, err = q.GetPayment(ctx, paymentID)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			} else if err == nil {
				_, err = q.GetOrderForUpdate(ctx, snapshot.OrderID)
				if err == nil {
					var payments []db.Payment
					payments, err = q.ListPaymentsByOrderForUpdate(ctx, snapshot.OrderID)
					if err == nil {
						for _, candidate := range payments {
							if candidate.ID == paymentID {
								payment = candidate
								break
							}
						}
					}
				}
				if err == nil && payment.ID > 0 {
					payment, err = requirePaymentReconciliation(ctx, q, paymentID, "job_exhausted", lastError)
					reconcileRequired = err == nil
				}
				if err == nil && payment.ID > 0 {
					_, err = q.CreatePaymentReconciliationFailure(ctx, db.CreatePaymentReconciliationFailureParams{
						PaymentID: paymentID, Provider: provider, Reason: "job_exhausted",
						RiverJobID: pgtype.Int8{Int64: job.ID, Valid: true}, Attempt: int32(job.Attempt), LastError: lastError,
					})
				}
			}
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
	}
	if err != nil {
		h.log.WithContext(ctx).Errorw("msg", "persist discarded payment job failed", "job_id", job.ID, "payment_id", paymentID, "error", err)
	} else {
		if reconcileRequired {
			h.invalidatePaymentCaches(ctx, payment)
			observability.PaymentReconcileRequired(ctx, provider)
		}
		observability.RiverJobDiscarded(ctx, job.Kind)
		h.log.WithContext(ctx).Errorw("msg", "payment reconciliation job discarded", "event", "river_job_discarded", "job_id", job.ID, "payment_id", paymentID, "provider", provider, "reconcile_required", reconcileRequired)
	}
	return nil
}

func (h *PaymentRiverErrorHandler) invalidatePaymentCaches(ctx context.Context, payment db.Payment) {
	batchCacheInvalidations(ctx, func(ctx context.Context) {
		repo := &PaymentRepo{data: &Data{q: db.New(h.pool), rdb: h.rdb}, log: h.log}
		repo.invalidatePayment(ctx, payment)
		repo.invalidateOrder(ctx, payment.OrderID)
	})
}

func (h *PaymentRiverErrorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicValue any, trace string) *river.ErrorHandlerResult {
	return h.HandleError(ctx, job, fmt.Errorf("panic: %v\n%s", panicValue, trace))
}
