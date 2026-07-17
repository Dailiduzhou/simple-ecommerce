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
	if job == nil || job.Kind != biz.CheckPayJobKind || job.Attempt < job.MaxAttempts {
		return nil
	}
	var args biz.CheckPayArgs
	if err := json.Unmarshal(job.EncodedArgs, &args); err != nil {
		h.log.WithContext(ctx).Errorw("msg", "decode discarded payment job failed", "job_id", job.ID, "error", err)
		return nil
	}
	lastError := "payment reconciliation job exhausted"
	if workErr != nil {
		lastError = workErr.Error()
	}
	var payment db.Payment
	tx, err := h.pool.Begin(ctx)
	if err == nil {
		q := db.New(tx)
		payment, err = q.GetPaymentForUpdate(ctx, args.PaymentID)
		if err == nil {
			_, markErr := q.MarkPaymentReconcileRequired(ctx, args.PaymentID)
			if markErr != nil && !errors.Is(markErr, pgx.ErrNoRows) {
				err = markErr
			}
		}
		if err == nil {
			_, err = q.CreatePaymentReconciliationFailure(ctx, db.CreatePaymentReconciliationFailureParams{PaymentID: args.PaymentID, Provider: args.Provider, RiverJobID: pgtype.Int8{Int64: job.ID, Valid: true}, Attempt: int32(job.Attempt), LastError: lastError})
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
	}
	if err != nil {
		h.log.WithContext(ctx).Errorw("msg", "persist discarded payment job failed", "job_id", job.ID, "payment_id", args.PaymentID, "error", err)
	} else {
		h.invalidatePaymentCaches(ctx, payment)
		observability.RiverJobDiscarded(ctx, job.Kind)
		observability.PaymentReconcileRequired(ctx, args.Provider)
		h.log.WithContext(ctx).Errorw("msg", "payment reconciliation job discarded", "event", "river_job_discarded", "job_id", job.ID, "payment_id", args.PaymentID, "provider", args.Provider)
	}
	return nil
}

func (h *PaymentRiverErrorHandler) invalidatePaymentCaches(ctx context.Context, payment db.Payment) {
	if h.rdb == nil || payment.ID == 0 {
		return
	}
	keys := []string{
		redisKey("payment", payment.ID),
		redisKey("payment", "order", payment.OrderID),
		redisKey("payment", "order", payment.OrderID, "active", payment.PayChannel),
		redisKey("order", payment.OrderID),
	}
	if payment.OutTradeNo.Valid {
		keys = append(keys, redisKey("payment", "out_trade_no", payment.OutTradeNo.String))
	}
	if order, err := db.New(h.pool).GetOrder(ctx, payment.OrderID); err == nil {
		keys = append(keys, redisKey("order", "user", order.ID, order.UserID))
		if order.OutTradeNo.Valid {
			keys = append(keys, redisKey("order", "no", order.OutTradeNo.String))
		}
		bumpCacheGeneration(ctx, h.rdb, h.log, redisKey("order", "user", order.UserID, "gen"))
		bumpCacheGeneration(ctx, h.rdb, h.log, redisKey("order", "user", "ongoing", order.UserID, "gen"))
	}
	if err := h.rdb.Unlink(ctx, keys...).Err(); err != nil {
		h.log.WithContext(ctx).Errorw("msg", "invalidate discarded payment caches failed", "payment_id", payment.ID, "error", err)
	}
}

func (h *PaymentRiverErrorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicValue any, trace string) *river.ErrorHandlerResult {
	return h.HandleError(ctx, job, fmt.Errorf("panic: %v\n%s", panicValue, trace))
}
