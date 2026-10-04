package data

import (
	"context"
	"errors"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5"
)

var _ biz.OrderFulfillmentRepo = (*OrderRepo)(nil)

func (r *OrderRepo) ApplyFulfillment(ctx context.Context, actor biz.Actor, input biz.OrderFulfillmentInput) (*biz.OrderFulfillmentAction, error) {
	if err := input.Validate(actor); err != nil {
		return nil, err
	}
	var result db.OrderFulfillmentAction
	var changed db.Order
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		// The audit action references its actor. Lock that user before the order
		// so concurrent account deletion cannot invert the FK lock order.
		if _, err := q.LockUserForReference(ctx, actor.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return biz.ErrOrderNotFound
			}
			return err
		}
		order, err := q.GetOrderForUpdate(ctx, input.OrderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if !actor.Admin && actor.ID != order.UserID {
			return biz.ErrOrderNotFound
		}
		result, err = q.GetOrderFulfillmentAction(ctx, db.GetOrderFulfillmentActionParams{OrderID: order.ID, IdempotencyKey: input.IdempotencyKey})
		if err == nil {
			if result.ActorID != actor.ID || result.Action != input.Action || result.Reason != input.Reason || result.Carrier != input.Carrier || result.TrackingNumber != input.TrackingNumber {
				return biz.ErrIdempotencyKeyConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if (input.Action == biz.OrderActionShip && order.Status != biz.OrderStatusPaid) ||
			(input.Action == biz.OrderActionComplete && order.Status != biz.OrderStatusShipped) || !order.PaidPaymentID.Valid {
			return biz.ErrOrderFulfillmentConflict
		}
		// Match refund/settlement lock order. A prepared refund is durable before
		// its gateway call; it must prevent dispatch while money may be returning.
		payment, err := q.GetPaymentForUpdate(ctx, order.PaidPaymentID.Int64)
		if err != nil {
			return err
		}
		if payment.OrderID != order.ID || payment.UserID != order.UserID || payment.AmountMinor != order.TotalAmountMinor || payment.Currency != order.Currency || payment.Status != biz.PaymentStatusSuccess {
			return biz.ErrOrderFulfillmentConflict
		}
		if payment.ReconciliationStatus != biz.ReconciliationStatusNone && payment.ReconciliationStatus != biz.ReconciliationStatusResolved {
			return biz.ErrPaymentReconciliationRequired
		}
		refund, err := q.GetOrderRefundByPaymentID(ctx, order.PaidPaymentID)
		if err == nil && refund.Status != biz.PaymentRefundStatusFailed {
			return biz.ErrOrderFulfillmentConflict
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if input.Action == biz.OrderActionShip {
			changed, err = q.MarkOrderShipped(ctx, order.ID)
		} else {
			changed, err = q.MarkOrderCompleted(ctx, order.ID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.ErrOrderFulfillmentConflict
		}
		if err != nil {
			return err
		}
		result, err = q.CreateOrderFulfillmentAction(ctx, db.CreateOrderFulfillmentActionParams{
			OrderID: order.ID, ActorID: actor.ID, Action: input.Action, FromStatus: order.Status, ToStatus: changed.Status,
			IdempotencyKey: input.IdempotencyKey, Reason: input.Reason, Carrier: input.Carrier, TrackingNumber: input.TrackingNumber,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if changed.ID > 0 {
		r.invalidateOrder(ctx, toBizOrder(changed))
	}
	action := toBizFulfillmentAction(result)
	return &action, nil
}

func (r *OrderRepo) ListFulfillmentActions(ctx context.Context, actor biz.Actor, orderID int64) ([]biz.OrderFulfillmentAction, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	order, err := r.data.DB(ctx).GetOrder(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	if !actor.Admin && actor.ID != order.UserID {
		return nil, biz.ErrOrderNotFound
	}
	rows, err := r.data.DB(ctx).ListOrderFulfillmentActions(ctx, orderID)
	if err != nil {
		return nil, err
	}
	result := make([]biz.OrderFulfillmentAction, len(rows))
	for i, row := range rows {
		result[i] = toBizFulfillmentAction(row)
	}
	return result, nil
}

func toBizFulfillmentAction(row db.OrderFulfillmentAction) biz.OrderFulfillmentAction {
	return biz.OrderFulfillmentAction{ID: row.ID, OrderID: row.OrderID, ActorID: row.ActorID, Action: row.Action,
		FromStatus: row.FromStatus, ToStatus: row.ToStatus, IdempotencyKey: row.IdempotencyKey, Reason: row.Reason,
		Carrier: row.Carrier, TrackingNumber: row.TrackingNumber, CreatedAt: row.CreatedAt.Time}
}
