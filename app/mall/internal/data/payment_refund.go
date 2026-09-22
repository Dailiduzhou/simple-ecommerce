package data

import (
	"context"
	"errors"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
)

// Cancelled/refunded orders may still need a compensating refund for a late or
// duplicate receipt. Their inventory has already been restored. Other states
// (notably shipped/completed) need a separate returns workflow, not this API.
func refundableOrderStatus(status string) bool {
	switch status {
	case biz.OrderStatusPaid, biz.OrderStatusCancelled, biz.OrderStatusRefunded:
		return true
	default:
		return false
	}
}

// settleOrderAfterRefund runs with the order and its payments locked. Refunding
// one payment must not cancel an order still covered by another successful
// payment. In-flight payments must be resolved before refunding. Only the last
// backing payment restores stock, once.
func settleOrderAfterRefund(ctx context.Context, q db.Querier, order db.Order, payments []db.Payment, paymentID int64) (bool, error) {
	if !refundableOrderStatus(order.Status) {
		return false, biz.ErrPaymentStateConflict
	}
	if hasOtherActivePayment(payments, paymentID) {
		return false, biz.ErrPaymentStateConflict
	}
	if order.Status != biz.OrderStatusPaid {
		return false, nil
	}
	for _, payment := range payments {
		if payment.ID == paymentID {
			continue
		}
		switch payment.Status {
		case biz.PaymentStatusSuccess:
			return false, nil
		}
	}
	if _, err := q.MarkOrderRefunded(ctx, order.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, biz.ErrPaymentStateConflict
		}
		return false, err
	}
	if err := q.RestoreOrderItemStock(ctx, order.ID); err != nil {
		return false, err
	}
	return true, nil
}

func hasOtherActivePayment(payments []db.Payment, paymentID int64) bool {
	for _, payment := range payments {
		if payment.ID == paymentID {
			continue
		}
		switch payment.Status {
		case biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending:
			return true
		}
	}
	return false
}
