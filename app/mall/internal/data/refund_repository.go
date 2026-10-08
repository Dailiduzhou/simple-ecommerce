package data

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// settlePaymentRefund is the sole local settlement of a provider-confirmed
// refund, used by both the API and query recovery. The caller holds the order
// and payment locks. The returned boolean describes an actual stock mutation.
func settlePaymentRefund(ctx context.Context, q db.Querier, order db.Order, payment db.Payment, refund db.OrderRefund) (db.Payment, bool, error) {
	if refund.PaymentID.Int64 != payment.ID || refund.OrderID != order.ID || payment.OrderID != order.ID ||
		refund.UserID != payment.UserID || refund.Currency != payment.Currency ||
		refund.TotalAmountMinor != payment.AmountMinor || refund.RefundAmountMinor != payment.AmountMinor {
		return db.Payment{}, false, biz.ErrPaymentStateConflict
	}
	if payment.Status == biz.PaymentStatusRefunded && refund.Status == biz.PaymentRefundStatusSuccess {
		return payment, false, nil
	}
	if payment.Status != biz.PaymentStatusSuccess {
		return db.Payment{}, false, biz.ErrPaymentStateConflict
	}
	if err := biz.ValidateRefundSettlement(biz.RefundPurpose(refund.Purpose), order.Status, order.PaidPaymentID.Int64, payment.ID); err != nil {
		return db.Payment{}, false, err
	}
	stockRestored := refund.Purpose == string(biz.RefundOrderCancel)
	if stockRestored {
		if _, err := q.MarkOrderRefunded(ctx, order.ID); err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return db.Payment{}, false, biz.ErrPaymentStateConflict
			}
			return db.Payment{}, false, err
		}
		if err := q.RestoreOrderItemStock(ctx, order.ID); err != nil {
			return db.Payment{}, false, err
		}
	}
	if _, err := q.MarkOrderRefundSuccess(ctx, db.MarkOrderRefundSuccessParams{
		ID: refund.ID, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
	}); err != nil {
		return db.Payment{}, false, err
	}
	changed, err := q.ConfirmPaymentRefunded(ctx, payment.ID)
	return changed, stockRestored, err
}

func (r *PaymentRepo) PreparePaymentRefund(ctx context.Context, paymentID int64, outRefundNo string) (*biz.PaymentDO, *biz.PaymentRefund, error) {
	var payment db.Payment
	var refund db.OrderRefund
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		// Resolve the refund's user from the database, not a cached payment.
		// Creating the refund takes a user FK lock: acquire it before the order
		// and payment locks, matching account deletion's user-first lock order.
		snapshot, err := q.GetPayment(ctx, paymentID)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return biz.ErrPaymentNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.LockUserForReference(ctx, snapshot.UserID); err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrPaymentNotFound
			}
			return err
		}
		// Read the settlement facts again under the order/payment locks. The
		// initial snapshot is used only to establish the user lock, not state.
		order, err := q.GetOrderForUpdateByPaymentID(ctx, paymentID)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return biz.ErrPaymentNotFound
		}
		if err != nil {
			return err
		}
		payment, err = q.GetPaymentForUpdate(ctx, paymentID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrPaymentNotFound
			}
			return err
		}
		if payment.UserID != snapshot.UserID || payment.OrderID != order.ID {
			return biz.ErrPaymentStateConflict
		}
		refund, err = q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: paymentID, Valid: true})
		if err == nil {
			if refund.OrderID != payment.OrderID || refund.UserID != payment.UserID ||
				refund.TotalAmountMinor != payment.AmountMinor || refund.RefundAmountMinor != payment.AmountMinor ||
				refund.Currency != payment.Currency {
				return biz.ErrPaymentStateConflict
			}
			if payment.Status == biz.PaymentStatusRefunded && refund.Status != biz.PaymentRefundStatusSuccess {
				return biz.ErrPaymentStateConflict
			}
			if payment.Status != biz.PaymentStatusSuccess && payment.Status != biz.PaymentStatusRefunded {
				return biz.ErrPaymentStateConflict
			}
			if payment.Status != biz.PaymentStatusRefunded {
				if err := biz.ValidateRefundSettlement(biz.RefundPurpose(refund.Purpose), order.Status, order.PaidPaymentID.Int64, payment.ID); err != nil {
					return err
				}
			}
			if refund.Status == biz.PaymentRefundStatusFailed {
				// Commit pending before any retry can move money. Keep the original number.
				refund, err = q.RetryOrderRefund(ctx, refund.ID)
				return err
			}
			return nil
		}
		if !stderrors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if payment.Status != biz.PaymentStatusSuccess {
			return biz.ErrPaymentStateConflict
		}
		purpose, err := biz.DecideRefundPurpose(order.Status, order.PaidPaymentID.Int64, payment.ID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(outRefundNo) == "" {
			return errors.BadRequest("OUT_REFUND_NO_REQUIRED", "out_refund_no is required")
		}
		refund, err = q.CreateOrderRefund(ctx, db.CreateOrderRefundParams{
			PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
			OrderID:   payment.OrderID, UserID: payment.UserID, OutRefundNo: outRefundNo,
			TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
			Currency: payment.Currency, Purpose: string(purpose),
		})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return toBizPayment(payment), toBizPaymentRefund(refund), nil
}

func (r *PaymentRepo) RecordPaymentRefundError(ctx context.Context, refundID int64, lastError string, definitive bool) error {
	_, err := querierFromContext(ctx, r.data.q).RecordOrderRefundError(ctx, db.RecordOrderRefundErrorParams{
		Definitive: definitive, LastError: lastError, ID: refundID,
	})
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (r *PaymentRepo) ListStalePendingRefunds(ctx context.Context, olderThan time.Duration, limit int) ([]biz.PaymentRefund, error) {
	if olderThan <= 0 {
		olderThan = 10 * time.Minute
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := querierFromContext(ctx, r.data.q).ListStalePendingRefunds(ctx, db.ListStalePendingRefundsParams{
		OlderThanSeconds: olderThan.Seconds(), LimitRows: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	refunds := make([]biz.PaymentRefund, 0, len(rows))
	for _, row := range rows {
		refunds = append(refunds, *toBizPaymentRefund(row))
	}
	return refunds, nil
}

func (r *PaymentRepo) ApplyPaymentRefund(ctx context.Context, paymentID, refundID int64) error {
	var changed db.Payment
	var order db.Order
	stockRestored := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		// Lock the order row before the payment row: every other
		// order-payment transaction locks orders first, and inverting the
		// order here would open a deadlock window against them.
		var err error
		order, err = q.GetOrderForUpdateByPaymentID(ctx, paymentID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrPaymentNotFound
			}
			return err
		}
		current, err := q.GetPaymentForUpdate(ctx, paymentID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrPaymentNotFound
			}
			return err
		}
		refund, err := q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: paymentID, Valid: true})
		if err != nil {
			return err
		}
		if refund.ID != refundID {
			return biz.ErrPaymentStateConflict
		}
		changed, stockRestored, err = settlePaymentRefund(ctx, q, order, current, refund)
		return err
	})
	if err != nil {
		return err
	}
	batchCacheInvalidations(ctx, func(ctx context.Context) {
		r.invalidatePayment(ctx, changed)
		(&OrderRepo{data: r.data, log: r.log}).invalidateOrder(ctx, toBizOrder(order))
		if stockRestored {
			invalidateProductCachesForOrder(ctx, r.data, r.log, order.ID)
		}
	})
	observability.PaymentTransition(ctx, biz.PaymentStatusSuccess, biz.PaymentStatusRefunded, "provider_refund", strings.SplitN(changed.PayChannel, ":", 2)[0])
	return nil
}

func toBizPaymentRefund(row db.OrderRefund) *biz.PaymentRefund {
	return &biz.PaymentRefund{
		ID: row.ID, PaymentID: row.PaymentID.Int64, OrderID: row.OrderID, UserID: row.UserID,
		OutRefundNo: row.OutRefundNo, TotalAmount: row.TotalAmountMinor, RefundAmount: row.RefundAmountMinor,
		Currency: row.Currency, Reason: row.Reason, Purpose: biz.RefundPurpose(row.Purpose), Status: row.Status, LastError: row.LastError,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}
