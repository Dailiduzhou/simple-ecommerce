package data

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
)

var _ biz.OrderExpiryRepo = (*OrderExpiryRepo)(nil)

type OrderExpiryRepo struct {
	data *Data
	tx   biz.TxManager
	jobs biz.PaymentMQRepo
	log  *log.Helper
}

func NewOrderExpiryRepo(data *Data, tx biz.TxManager, jobs biz.PaymentMQRepo, logger log.Logger) *OrderExpiryRepo {
	return &OrderExpiryRepo{data: data, tx: tx, jobs: jobs, log: log.NewHelper(logger)}
}

func (r *OrderExpiryRepo) ExpireOrder(ctx context.Context, orderID int64) error {
	if orderID <= 0 {
		return biz.ErrOrderNotFound
	}
	var order db.Order
	var changedPayments []db.Payment
	cancelled := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		var err error
		order, err = q.GetOrderForUpdate(ctx, orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if order.Status != biz.OrderStatusPendingPayment {
			return nil
		}
		// River normally schedules this job at expires_at, but workers must not
		// trust queue timing alone. Re-check under the order row lock so an
		// early delivery cannot cancel a still-payable order. The comparison
		// runs in SQL so all instances agree with the database clock.
		expired, err := q.OrderIsExpired(ctx, orderID)
		if err != nil {
			return err
		}
		if !expired {
			return biz.ErrOrderNotExpired
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		for _, payment := range payments {
			if payment.ReconciliationStatus == biz.ReconciliationStatusRequired {
				return biz.ErrPaymentReconciliationRequired
			}
		}
		for _, payment := range payments {
			switch payment.Status {
			case biz.PaymentStatusSuccess:
				_, err = q.MarkOrderPaid(ctx, orderID)
				return err
			case biz.PaymentStatusRefunded:
				// A refunded payment on a still-pending order is an anomaly:
				// money went back while the order never left pending_payment.
				// Flag it for manual reconciliation; the worker keeps snoozing
				// on ErrPaymentReconciliationRequired until it is resolved.
				if _, err := requirePaymentReconciliation(ctx, q, payment.ID,
					"refunded_on_pending_order", "refunded payment found on a pending order during expiry"); err != nil {
					return err
				}
				provider := payment.PayChannel
				if method, parseErr := biz.ParsePaymentMethod(payment.PayChannel); parseErr == nil {
					provider = method.Provider
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, Provider: provider, Attempt: 1,
					Reason: "refunded_on_pending_order", LastError: "refunded payment found on a pending order during expiry",
				}); err != nil {
					return err
				}
				return biz.ErrPaymentReconciliationRequired
			}
		}
		hasActive := false
		for _, payment := range payments {
			switch payment.Status {
			case biz.PaymentStatusCreating, biz.PaymentStatusPending:
				payment, err = q.MarkPaymentClosePending(ctx, payment.ID)
				if err != nil {
					return err
				}
				changedPayments = append(changedPayments, payment)
				hasActive = true
			case biz.PaymentStatusClosePending:
				hasActive = true
			default:
				continue
			}
			method, err := biz.ParsePaymentMethod(payment.PayChannel)
			if err != nil {
				return err
			}
			if r.jobs == nil {
				return fmt.Errorf("payment mq is not configured")
			}
			if _, err := r.jobs.EnqueueClosePayTx(ctx, biz.ClosePayArgs{
				PaymentID: payment.ID, Provider: method.Provider, Reason: "order_expired",
			}, time.Time{}); err != nil {
				return err
			}
		}
		if hasActive {
			return nil
		}
		if _, err := q.MarkOrderCancelling(ctx, orderID); err != nil {
			return err
		}
		if err := q.RestoreOrderItemStock(ctx, orderID); err != nil {
			return err
		}
		if _, err = q.MarkOrderCancelled(ctx, orderID); err != nil {
			return err
		}
		cancelled = true
		return nil
	})
	if err != nil {
		return err
	}
	if order.ID > 0 {
		orderRepo := &OrderRepo{data: r.data, log: r.log}
		orderRepo.invalidateOrder(ctx, toBizOrder(order))
	}
	paymentRepo := &PaymentRepo{data: r.data, log: r.log}
	for _, payment := range changedPayments {
		paymentRepo.invalidatePayment(ctx, payment)
	}
	if cancelled {
		invalidateProductCachesForOrder(ctx, r.data, r.log, orderID)
	}
	return nil
}

// ReapOverdueOrders re-enqueues expiry jobs for pending_payment orders whose
// payment window ended more than grace ago. River's ByArgs uniqueness skips
// inserts while an active expire_order job for the same order exists, so this
// is safe to run repeatedly and heals jobs lost to queue errors or discard.
func (r *OrderExpiryRepo) ReapOverdueOrders(ctx context.Context, grace time.Duration, limit int) ([]int64, error) {
	if grace <= 0 {
		grace = 5 * time.Minute
	}
	if limit <= 0 {
		limit = 100
	}
	if r.jobs == nil {
		return nil, fmt.Errorf("order mq is not configured")
	}
	orderIDs, err := r.data.q.ListOverduePendingOrders(ctx, db.ListOverduePendingOrdersParams{
		GraceSeconds: grace.Seconds(), LimitRows: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	requeued := make([]int64, 0, len(orderIDs))
	for _, id := range orderIDs {
		job, err := r.jobs.EnqueueExpireOrder(ctx, biz.ExpireOrderArgs{OrderID: id}, time.Time{})
		if err != nil {
			r.log.WithContext(ctx).Errorw("msg", "re-enqueue overdue expire order failed", "order_id", id, "error", err)
			continue
		}
		if !job.Deduplicated {
			requeued = append(requeued, id)
		}
	}
	if len(requeued) > 0 {
		r.log.WithContext(ctx).Warnw("msg", "reaper found overdue pending orders", "count", len(requeued), "grace", grace.String())
	}
	return requeued, nil
}
