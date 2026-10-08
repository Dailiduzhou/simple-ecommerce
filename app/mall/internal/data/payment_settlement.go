package data

import (
	"context"
	stderrors "errors"
	"strings"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *PaymentRepo) ApplyPayQuery(ctx context.Context, args biz.CheckPayArgs, result *biz.PaymentQueryResult) error {
	var changed db.Payment
	var fromStatus, provider, event string
	var orderID int64
	orderCancelled := false
	stockRestored := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		snapshot, err := q.GetPayment(ctx, args.PaymentID)
		if err != nil {
			return err
		}
		orderID = snapshot.OrderID
		order, err := q.GetOrderForUpdate(ctx, snapshot.OrderID)
		if err != nil {
			return err
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, snapshot.OrderID)
		if err != nil {
			return err
		}
		var payment db.Payment
		found := false
		for _, candidate := range payments {
			if candidate.ID == args.PaymentID {
				payment = candidate
				found = true
				break
			}
		}
		if !found {
			return biz.ErrPaymentNotFound
		}
		method, parseErr := biz.ParsePaymentMethod(payment.PayChannel)
		if parseErr != nil {
			return parseErr
		}
		if mismatch := validateProviderResult(payment, method, result); mismatch != "" {
			reason := reconciliationReasonForMismatch(mismatch)
			provider, fromStatus, event = method.Provider, payment.Status, reason
			if mismatch == "amount mismatch" {
				observability.PaymentAmountMismatch(ctx, method.Provider)
			}
			observability.PaymentReconcileRequired(ctx, method.Provider)
			changed, err = requirePaymentReconciliation(ctx, q, payment.ID, reason, mismatch)
			if err != nil {
				return err
			}
			if recordErr := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
				PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
				Attempt: max(1, args.PollCount), Reason: reason, LastError: mismatch,
			}); recordErr != nil {
				return recordErr
			}
			r.log.WithContext(ctx).Errorw("msg", "payment provider result mismatch", "event", "payment_reconcile_required", "payment_id", payment.ID, "provider", method.Provider, "reason", mismatch)
			return markNotificationProcessed(ctx, q, args.NotificationID)
		}
		switch result.TradeState {
		case biz.TradeStateSuccess:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_success"
			if result.TransactionID == "" {
				return errors.BadRequest("PAYMENT_TRANSACTION_ID_REQUIRED", "successful payment requires transaction id")
			}
			if payment.Status == biz.PaymentStatusSuccess && payment.ThirdPartyTxID.String == result.TransactionID {
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			if payment.Status == biz.PaymentStatusSuccess {
				changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "provider_mismatch", "successful payment transaction id changed")
				if err != nil {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
					Attempt: max(1, args.PollCount), Reason: "provider_mismatch",
					LastError: "successful payment transaction id changed",
				}); err != nil {
					return err
				}
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			if payment.Status == biz.PaymentStatusRefunded {
				return biz.ErrPaymentStateConflict
			}
			otherSuccess := false
			for _, candidate := range payments {
				if candidate.ID != payment.ID && (candidate.Status == biz.PaymentStatusSuccess || candidate.Status == biz.PaymentStatusRefunded) {
					otherSuccess = true
					break
				}
			}
			changed, err = q.RecordPaymentSuccess(ctx, db.RecordPaymentSuccessParams{
				ID: payment.ID, ThirdPartyTxID: pgtype.Text{String: result.TransactionID, Valid: true},
			})
			if err != nil {
				return err
			}
			decision, err := biz.DecidePaymentSuccess(order.Status, otherSuccess)
			if err != nil {
				return err
			}
			if decision.MarkOrderPaid {
				if _, err := q.MarkOrderPaid(ctx, db.MarkOrderPaidParams{ID: payment.OrderID, PaidPaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}}); err != nil {
					return err
				}
			}
			if decision.ReconciliationReason != "" {
				event = decision.ReconciliationReason
				changed, err = requirePaymentReconciliation(ctx, q, payment.ID, event, decision.ReconciliationDetail)
				if err != nil {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
					Attempt: max(1, args.PollCount), Reason: event, LastError: decision.ReconciliationDetail,
				}); err != nil {
					return err
				}
				if otherSuccess {
					observability.PaymentConflict(ctx, method.Provider)
				}
				observability.PaymentReconcileRequired(ctx, method.Provider)
			}
		case biz.TradeStateClosed, biz.TradeStateRevoked:
			if payment.Status == biz.PaymentStatusRefunded {
				if result.TransactionID == "" || result.TransactionID != payment.ThirdPartyTxID.String {
					return biz.ErrPaymentStateConflict
				}
				refund, err := q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: payment.ID, Valid: true})
				if err != nil {
					return err
				}
				if refund.Status != biz.PaymentRefundStatusSuccess || refund.OrderID != payment.OrderID || refund.UserID != payment.UserID || refund.TotalAmountMinor != payment.AmountMinor || refund.RefundAmountMinor != payment.AmountMinor || refund.Currency != payment.Currency {
					return biz.ErrPaymentStateConflict
				}
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}

			provider, fromStatus, event = method.Provider, payment.Status, "provider_closed"
			if payment.Status == biz.PaymentStatusClosed {
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			changed, err = q.MarkPaymentClosed(ctx, payment.ID)
			if err != nil {
				return paymentStateAfterCAS(ctx, q, payment.ID, biz.PaymentStatusClosed)
			}
			orderCancelled, err = finalizeOrderAfterPaymentInactive(ctx, q, order, payments, payment)
			if err != nil {
				return err
			}
		case biz.TradeStatePayError:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_failed"
			changed, err = q.MarkPaymentFailed(ctx, db.MarkPaymentFailedParams{
				ID: payment.ID, LastError: pgtype.Text{String: result.TradeStateDesc, Valid: result.TradeStateDesc != ""},
			})
			if stderrors.Is(err, pgx.ErrNoRows) {
				// Idempotent retry: the payment already left the active set.
				if payment.Status != biz.PaymentStatusFailed && payment.Status != biz.PaymentStatusClosed {
					return biz.ErrPaymentStateConflict
				}
				err = nil
			}
			if err != nil {
				return err
			}
			// A failed close_pending payment comes from the close flow (order
			// expiry, api close, or poll exhaustion); nobody revisits the order
			// afterwards, so settle it here exactly like a provider close.
			if payment.Status == biz.PaymentStatusClosePending || args.Trigger == "close_pay" {
				orderCancelled, err = finalizeOrderAfterPaymentInactive(ctx, q, order, payments, payment)
				if err != nil {
					return err
				}
			}
		case biz.TradeStateRefund:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_refund"
			if payment.Status == biz.PaymentStatusRefunded {
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			if payment.Status != biz.PaymentStatusSuccess {
				return biz.ErrPaymentStateConflict
			}
			refund, refundErr := q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: payment.ID, Valid: true})
			if refundErr != nil && !stderrors.Is(refundErr, pgx.ErrNoRows) {
				return refundErr
			}
			if refundErr != nil {
				// A refund we never initiated moved money outside our control;
				// a human must reconcile it rather than silently flipping state.
				changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "provider_side_refund",
					"provider reported a refund that was never initiated locally")
				if err != nil {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
					Attempt: max(1, args.PollCount), Reason: "provider_side_refund",
					LastError: "provider reported a refund that was never initiated locally",
				}); err != nil {
					return err
				}
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			changed, stockRestored, err = settlePaymentRefund(ctx, q, order, payment, refund)
			if err != nil {
				return err
			}
		default:
			provider, fromStatus, event = method.Provider, payment.Status, "unknown_provider_state"
			changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "unknown_provider_state", result.RawTradeState)
			if err != nil {
				return err
			}
			if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
				PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
				Attempt: max(1, args.PollCount), Reason: "unknown_provider_state",
				LastError: "unsupported provider state " + result.RawTradeState,
			}); err != nil {
				return err
			}
		}
		return markNotificationProcessed(ctx, q, args.NotificationID)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if stderrors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_payments_third_party_tx_id_channel" {
			// The provider reported this transaction id for another payment;
			// both cannot be the same money movement. The unique violation
			// aborted the transaction, so settle state in a fresh one instead
			// of letting the job retry the doomed UPDATE forever.
			return r.MarkReconciliationRequired(ctx, biz.ReconciliationFailure{
				PaymentID: args.PaymentID, NotificationID: args.NotificationID, Provider: provider,
				Attempt: max(1, args.PollCount), Reason: "duplicate_third_party_tx",
				LastError: "third party transaction id is already recorded on another payment",
			})
		}
		return err
	}
	batchCacheInvalidations(ctx, func(ctx context.Context) {
		if err == nil && changed.ID > 0 {
			observability.PaymentTransition(ctx, fromStatus, changed.Status, event, provider)
			r.invalidatePayment(ctx, changed)
			r.invalidateOrder(ctx, changed.OrderID)
		}
		if err == nil && (orderCancelled || stockRestored) {
			if changed.ID == 0 {
				r.invalidateOrder(ctx, orderID)
			}
			invalidateProductCachesForOrder(ctx, r.data, r.log, orderID)
		}
	})
	return err
}

// finalizeOrderAfterPaymentInactive settles the order after `current` left the
// active payment set (closed or failed): heal the order to paid when a sibling
// payment already succeeded, or cancel it and restore stock when nothing else
// can still pay for it. Returns true when the order was cancelled so callers
// can invalidate product caches after the transaction commits.
func finalizeOrderAfterPaymentInactive(ctx context.Context, q db.Querier, order db.Order, payments []db.Payment, current db.Payment) (bool, error) {
	var successfulPaymentID int64
	hasActive := false
	hasReconciliation := biz.ReconciliationNeedsReview(current.ReconciliationStatus)
	for _, candidate := range payments {
		if candidate.ID == current.ID {
			continue
		}
		if biz.ReconciliationNeedsReview(candidate.ReconciliationStatus) {
			hasReconciliation = true
		}
		switch candidate.Status {
		case biz.PaymentStatusSuccess:
			if !biz.ReconciliationNeedsReview(candidate.ReconciliationStatus) {
				successfulPaymentID = candidate.ID
			}
		case biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending:
			hasActive = true
		}
	}
	if successfulPaymentID > 0 {
		if order.Status == biz.OrderStatusPendingPayment {
			if _, err := q.MarkOrderPaid(ctx, db.MarkOrderPaidParams{ID: order.ID, PaidPaymentID: pgtype.Int8{Int64: successfulPaymentID, Valid: true}}); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if !hasActive && !hasReconciliation && order.Status == biz.OrderStatusPendingPayment {
		if _, err := q.MarkOrderCancelling(ctx, order.ID); err != nil {
			return false, err
		}
		if err := q.RestoreOrderItemStock(ctx, order.ID); err != nil {
			return false, err
		}
		if _, err := q.MarkOrderCancelled(ctx, order.ID); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func reconciliationReasonForMismatch(mismatch string) string {
	switch mismatch {
	case "amount mismatch":
		return "amount_mismatch"
	case "currency mismatch":
		return "currency_mismatch"
	default:
		return "provider_mismatch"
	}
}

func requirePaymentReconciliation(ctx context.Context, q db.Querier, paymentID int64, reason, detail string) (db.Payment, error) {
	payment, err := q.RequirePaymentReconciliation(ctx, db.RequirePaymentReconciliationParams{
		ID:                   paymentID,
		ReconciliationReason: pgtype.Text{String: reason, Valid: reason != ""},
		ReconciliationDetail: pgtype.Text{String: detail, Valid: detail != ""},
	})
	if stderrors.Is(err, pgx.ErrNoRows) {
		// The same unresolved anomaly is idempotent. New anomalies reopen resolved
		// cases and increment their version. Returning the current row
		// lets the surrounding transaction commit instead of retrying forever.
		current, loadErr := q.GetPayment(ctx, paymentID)
		if loadErr != nil {
			return db.Payment{}, loadErr
		}
		return current, nil
	}
	return payment, err
}

func validateProviderResult(payment db.Payment, method biz.PaymentMethod, result *biz.PaymentQueryResult) string {
	if result == nil {
		return "empty provider result"
	}
	if result.OutTradeNo != payment.OutTradeNo {
		return "out_trade_no mismatch"
	}
	if result.Method.Normalize().String() != method.String() {
		return "payment method mismatch"
	}
	// Amount and currency only matter when money moved; closed or failed
	// trades may omit them and must not be blocked on reconciliation.
	if result.TradeState == biz.TradeStateSuccess || result.TradeState == biz.TradeStateRefund {
		if result.Amount != payment.AmountMinor {
			return "amount mismatch"
		}
		if strings.ToUpper(result.Currency) != payment.Currency {
			return "currency mismatch"
		}
	}
	return ""
}

func paymentStateAfterCAS(ctx context.Context, q db.Querier, id int64, desired string) error {
	current, err := q.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == desired {
		return nil
	}
	return biz.ErrPaymentStateConflict
}
