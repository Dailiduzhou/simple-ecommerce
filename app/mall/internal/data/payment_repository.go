package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type PaymentRepo struct {
	data *Data
	tx   biz.TxManager
	jobs biz.PaymentMQRepo
	log  *log.Helper
}

func NewPaymentRepo(data *Data, tx biz.TxManager, logger log.Logger) *PaymentRepo {
	return NewPaymentRepoWithJobs(data, tx, nil, logger)
}

func NewPaymentRepoWithJobs(data *Data, tx biz.TxManager, jobs biz.PaymentMQRepo, logger log.Logger) *PaymentRepo {
	return &PaymentRepo{data: data, tx: tx, jobs: jobs, log: log.NewHelper(logger)}
}

func (r *PaymentRepo) CreatePayment(ctx context.Context, args biz.CreatePaymentArgs) (*biz.PaymentDO, error) {
	var row db.Payment
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		order, err := q.GetOrderForUpdate(ctx, args.OrderID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrOrderNotFound
			}
			return err
		}
		if order.UserID != args.UserID || order.TotalAmountMinor != args.Amount || order.Currency != args.Currency {
			return biz.ErrPaymentConflict
		}
		if order.Status == biz.OrderStatusPaid || order.Status == biz.OrderStatusShipped || order.Status == biz.OrderStatusCompleted {
			return biz.ErrOrderAlreadyPaid
		}
		if order.Status != biz.OrderStatusPendingPayment {
			return biz.ErrPaymentStateConflict
		}
		expired, err := q.OrderIsExpired(ctx, order.ID)
		if err != nil {
			return err
		}
		if expired {
			return biz.ErrOrderExpired
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, order.ID)
		if err != nil {
			return err
		}
		if err := recoverUnsupportedPayments(ctx, q, r.data, r.log, payments); err != nil {
			return err
		}
		for _, payment := range payments {
			if biz.ReconciliationNeedsReview(payment.ReconciliationStatus) {
				return biz.ErrPaymentReconciliationRequired
			}
			switch payment.Status {
			case biz.PaymentStatusSuccess, biz.PaymentStatusRefunded:
				return biz.ErrOrderAlreadyPaid
			case biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending:
				if payment.PayChannel == args.Method {
					row = payment
					return nil
				}
				return biz.ErrOrderHasActivePayment
			}
		}
		row, err = q.CreatePaymentWithOutTradeNo(ctx, db.CreatePaymentWithOutTradeNoParams{
			OrderID: args.OrderID, UserID: args.UserID, MerchantID: args.MerchantID, AmountMinor: args.Amount,
			Currency: args.Currency, Status: biz.PaymentStatusCreating, PayChannel: args.Method,
			OutTradeNo: args.OutTradeNo,
		})
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if stderrors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_payments_one_active_per_order" {
			existing, getErr := r.getActivePaymentByOrder(ctx, args.OrderID)
			if getErr == nil {
				if existing.Method == args.Method {
					return existing, nil
				}
				return nil, biz.ErrOrderHasActivePayment
			}
			return nil, biz.ErrPaymentConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.invalidatePayment(ctx, row)
	return payment, nil
}

func (r *PaymentRepo) ClaimPaymentPrepay(ctx context.Context, id int64, token string, leaseDuration time.Duration) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).ClaimPaymentPrepay(ctx, db.ClaimPaymentPrepayParams{
		ID: id, PrepayLeaseToken: pgtype.Text{String: token, Valid: token != ""},
		LeaseSeconds: leaseDuration.Seconds(),
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			currentRow, loadErr := querierFromContext(ctx, r.data.q).GetPayment(ctx, id)
			if loadErr != nil {
				return nil, loadErr
			}
			current := toBizPayment(currentRow)
			if current.Status == biz.PaymentStatusPending && current.Action.Type != "" {
				return current, nil
			}
			if current.Status == biz.PaymentStatusCreating && current.PrepayLeaseUntil != nil && current.PrepayLeaseUntil.After(time.Now()) {
				return nil, biz.ErrPaymentPrepayInProgress
			}
			return nil, biz.ErrPaymentStateConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.invalidatePayment(ctx, row)
	return payment, nil
}

func (r *PaymentRepo) FinalizePaymentPrepay(ctx context.Context, id int64, token string, action biz.PaymentAction) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).FinalizePaymentPrepay(ctx, db.FinalizePaymentPrepayParams{
		ID: id, PrepayLeaseToken: pgtype.Text{String: token, Valid: token != ""},
		ActionType:    pgtype.Text{String: string(action.Type), Valid: action.Type != ""},
		ActionPayload: action.Payload,
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			currentRow, loadErr := querierFromContext(ctx, r.data.q).GetPayment(ctx, id)
			if loadErr != nil {
				return nil, loadErr
			}
			current := toBizPayment(currentRow)
			if current.Status == biz.PaymentStatusPending && current.Action.Type != "" {
				return current, nil
			}
			return nil, biz.ErrPaymentStateConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.invalidatePayment(ctx, row)
	return payment, nil
}

func (r *PaymentRepo) RecordPaymentPrepayError(ctx context.Context, id int64, token, lastError string) error {
	_, err := querierFromContext(ctx, r.data.q).RecordPaymentPrepayError(ctx, db.RecordPaymentPrepayErrorParams{
		ID: id, PrepayLeaseToken: pgtype.Text{String: token, Valid: token != ""},
		LastError: pgtype.Text{String: lastError, Valid: lastError != ""},
	})
	return err
}

func (r *PaymentRepo) GetPayment(ctx context.Context, id int64) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", id, "gen"), func(gen int64) string { return redisKey("payment", id, "g", gen) }, func() (db.Payment, error) { return querierFromContext(ctx, r.data.q).GetPayment(ctx, id) })
}

func (r *PaymentRepo) GetPaymentByUser(ctx context.Context, id, userID int64) (*biz.PaymentDO, error) {
	payment, err := r.GetPayment(ctx, id)
	if err != nil {
		return nil, err
	}
	if payment.UserID != userID {
		return nil, biz.ErrPaymentNotFound
	}
	return payment, nil
}

func (r *PaymentRepo) GetLatestPaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "order", orderID, "gen"), func(gen int64) string { return redisKey("payment", "order", orderID, "g", gen) }, func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetLatestPaymentByOrder(ctx, orderID)
	})
}

func (r *PaymentRepo) GetActivePaymentByOrderMethod(ctx context.Context, orderID int64, method string) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "order", orderID, "gen"), func(gen int64) string { return redisKey("payment", "order", orderID, "active", method, "g", gen) }, func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetActivePaymentByOrderChannel(ctx, db.GetActivePaymentByOrderChannelParams{OrderID: orderID, PayChannel: method})
	})
}

func (r *PaymentRepo) getActivePaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).GetActivePaymentByOrder(ctx, orderID)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPaymentNotFound
	}
	if err != nil {
		return nil, err
	}
	return toBizPayment(row), nil
}

func (r *PaymentRepo) GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "out_trade_no", outTradeNo, "gen"), func(gen int64) string { return redisKey("payment", "out_trade_no", outTradeNo, "g", gen) }, func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetPaymentByOutTradeNo(ctx, outTradeNo)
	})
}

// GetOrderExpiry resolves the authoritative payment window for a payment even
// when the enqueue site could not embed the deadline (callback- and
// admin-triggered check jobs). A missing order maps to the zero time so the
// caller falls back to the legacy close-on-exhaustion behavior.
func (r *PaymentRepo) GetOrderExpiry(ctx context.Context, paymentID int64) (time.Time, error) {
	ts, err := querierFromContext(ctx, r.data.q).GetOrderExpiryByPaymentID(ctx, paymentID)
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return ts.Time, nil
}

// getPayment reads through the cache using keys versioned by a generation
// counter. Writers bump the generation after commit, so a reader that loaded a
// row just before a state change can only repopulate keys under the old
// generation, which no future read will ever consult.
func (r *PaymentRepo) getPayment(ctx context.Context, generationKey string, keyForGen func(int64) string, load func() (db.Payment, error)) (*biz.PaymentDO, error) {
	gen := readCacheGeneration(ctx, r.data.rdb, r.log, generationKey)
	key := generationCacheKey(gen, keyForGen(gen))
	return cacheAside(ctx, r.data, r.log, key, r.getCache, r.setCache, func() (*biz.PaymentDO, error) {
		row, err := load()
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, biz.ErrPaymentNotFound
		}
		if err != nil {
			return nil, err
		}
		return toBizPayment(row), nil
	})
}

func (r *PaymentRepo) MarkPayClosePending(ctx context.Context, args biz.CheckPayArgs) error {
	var changed db.Payment
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		row, err := q.MarkPaymentClosePending(ctx, args.PaymentID)
		if err != nil {
			if !stderrors.Is(err, pgx.ErrNoRows) {
				return err
			}
			current, loadErr := q.GetPayment(ctx, args.PaymentID)
			if loadErr != nil {
				return loadErr
			}
			if current.Status != biz.PaymentStatusClosePending {
				return biz.ErrPaymentStateConflict
			}
			row = current
		}
		method, err := biz.ParsePaymentMethod(row.PayChannel)
		if err != nil {
			return err
		}
		if r.jobs == nil {
			return fmt.Errorf("payment mq is not configured")
		}
		if _, err := r.jobs.EnqueueClosePayTx(ctx, biz.ClosePayArgs{
			PaymentID: row.ID, Provider: method.Provider, Reason: args.Trigger,
		}, time.Time{}); err != nil {
			return err
		}
		if err := markNotificationProcessed(ctx, q, args.NotificationID); err != nil {
			return err
		}
		changed = row
		return nil
	})
	if err == nil && changed.ID > 0 {
		r.invalidatePayment(ctx, changed)
	}
	return err
}

func (r *PaymentRepo) MarkReconciliationRequired(ctx context.Context, failure biz.ReconciliationFailure) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		reason := failure.Reason
		if reason == "" {
			reason = "job_exhausted"
		}
		row, err := requirePaymentReconciliation(ctx, q, failure.PaymentID, reason, failure.LastError)
		if err != nil {
			return err
		}
		if row.ID > 0 {
			r.invalidatePayment(ctx, row)
		}
		if err := createReconciliationFailure(ctx, q, failure); err != nil {
			return err
		}
		return markNotificationProcessed(ctx, q, failure.NotificationID)
	})
}

func (r *PaymentRepo) RecordReconciliationFailure(ctx context.Context, failure biz.ReconciliationFailure) error {
	return createReconciliationFailure(ctx, querierFromContext(ctx, r.data.q), failure)
}

func createReconciliationFailure(ctx context.Context, q db.Querier, failure biz.ReconciliationFailure) error {
	jobID := pgtype.Int8{}
	if failure.RiverJobID != nil {
		jobID = pgtype.Int8{Int64: *failure.RiverJobID, Valid: true}
	}
	reason := failure.Reason
	if reason == "" {
		reason = "job_exhausted"
	}
	_, err := q.CreatePaymentReconciliationFailure(ctx, db.CreatePaymentReconciliationFailureParams{
		PaymentID: failure.PaymentID, Provider: failure.Provider, Reason: reason,
		RiverJobID: jobID, Attempt: int32(max(1, failure.Attempt)), LastError: failure.LastError,
	})
	return err
}

func toBizPayment(row db.Payment) *biz.PaymentDO {
	payment := &biz.PaymentDO{
		ID: row.ID, OrderID: row.OrderID, UserID: row.UserID, MerchantID: row.MerchantID,
		Amount: row.AmountMinor, Currency: row.Currency, Status: row.Status, Method: row.PayChannel,
		OutTradeNo: row.OutTradeNo, ThirdPartyTxID: row.ThirdPartyTxID.String,
		ReconciliationVersion: row.ReconciliationVersion, ReconciliationStatus: row.ReconciliationStatus, ReconciliationReason: row.ReconciliationReason.String,
		ReconciliationDetail: row.ReconciliationDetail.String, PrepayLeaseToken: row.PrepayLeaseToken.String,
		PrepayAttempts: row.PrepayAttempts, LastError: row.LastError.String,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ActionType.Valid {
		payment.Action.Type = biz.PaymentActionType(row.ActionType.String)
		payment.Action.Payload = append(json.RawMessage(nil), row.ActionPayload...)
	}
	if row.PaidAt.Valid {
		paidAt := row.PaidAt.Time
		payment.PaidAt = &paidAt
	}
	if row.PrepayLeaseUntil.Valid {
		leaseUntil := row.PrepayLeaseUntil.Time
		payment.PrepayLeaseUntil = &leaseUntil
	}
	return payment
}
