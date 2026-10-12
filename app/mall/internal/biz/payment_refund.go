package biz

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/errors"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
)

func (uc *paymentUsecase) RefundPayment(ctx context.Context, paymentID int64) (*PaymentRefundResult, error) {
	if paymentID <= 0 {
		return nil, errors.BadRequest("PAYMENT_ID_REQUIRED", "payment_id is required")
	}
	if uc.idGen == nil {
		return nil, errors.InternalServer("PAYMENT_ID_GENERATOR_MISSING", "refund number generator is unavailable")
	}
	payment, err := uc.paymentRepo.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return nil, err
	}
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
	}
	if !capabilities.SupportsRefund {
		return nil, errors.New(501, "PAYMENT_REFUND_NOT_SUPPORTED", "provider does not support refund")
	}
	payment, refund, err := uc.paymentRepo.PreparePaymentRefund(
		ctx,
		paymentID,
		uc.idGen.GenerateOrderNo64("rfnd", payment.UserID),
	)
	if err != nil {
		return nil, err
	}
	result, err := uc.executeRefund(ctx, method, payment, refund)
	if err != nil {
		uc.recordRefundFailure(ctx, refund.ID, result, err)
	}
	return result, err
}

// executeRefund drives the gateway call and local settlement for a prepared
// refund record. It is shared by the admin refund API and the pending-refund
// reconciliation worker; both rely on the gateway's per-OutRefundNo
// idempotency to make re-issuing the same refund safe.
func (uc *paymentUsecase) executeRefund(ctx context.Context, method PaymentMethod, payment *PaymentDO, refund *PaymentRefund) (*PaymentRefundResult, error) {
	result := &PaymentRefundResult{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID,
		OutRefundNo: refund.OutRefundNo, Amount: refund.RefundAmount, Currency: refund.Currency,
	}
	if refund.Status == PaymentRefundStatusSuccess {
		result.Success = true
		return result, nil
	}
	result, err := uc.gateway.Refund(ctx, PaymentRefundRequest{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID,
		OutRefundNo: refund.OutRefundNo, Amount: refund.RefundAmount, Currency: refund.Currency,
		Reason: refund.Reason,
	})
	if err != nil {
		return result, err
	}
	if result == nil || !result.Success || result.Rejection {
		return result, errors.New(502, "PAYMENT_REFUND_FAILED", "payment provider did not confirm refund")
	}

	if err := uc.paymentRepo.ApplyPaymentRefund(ctx, payment.ID, refund.ID); err != nil {
		return nil, err
	}
	return result, nil
}

// ReconcilePendingRefunds retries refunds stuck in pending for longer than
// olderThan — for example when the gateway accepted the refund but the process
// crashed before ApplyPaymentRefund committed. Each record is re-prepared
// under the payment row lock and re-issued with the same OutRefundNo.
func (uc *paymentUsecase) ReconcilePendingRefunds(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if olderThan <= 0 {
		olderThan = 10 * time.Minute
	}
	if limit <= 0 {
		limit = 100
	}
	refunds, err := uc.paymentRepo.ListStalePendingRefunds(ctx, olderThan, limit)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, refund := range refunds {
		result, err := uc.reconcileRefund(ctx, refund)
		if err != nil {
			uc.log.WithContext(ctx).Errorw("msg", "reconcile pending refund failed", "payment_id", refund.PaymentID, "refund_id", refund.ID, "out_refund_no", refund.OutRefundNo, "error", err)
			observability.PaymentRefundReconcile(ctx, uc.recordRefundFailure(ctx, refund.ID, result, err))
			continue
		}

		observability.PaymentRefundReconcile(ctx, "settled")
		settled++
	}
	return settled, nil
}

func (uc *paymentUsecase) reconcileRefund(ctx context.Context, refund PaymentRefund) (*PaymentRefundResult, error) {
	payment, err := uc.paymentRepo.GetPayment(ctx, refund.PaymentID)
	if err != nil {
		return nil, err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return nil, err
	}
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
	}
	if !capabilities.SupportsRefund {
		return nil, fmt.Errorf("provider %s does not support refund", method.Provider)
	}
	// Re-preparing under the payment lock re-validates amounts and state, and
	// returns the existing refund record instead of creating a new one.
	current, prepared, err := uc.paymentRepo.PreparePaymentRefund(ctx, refund.PaymentID, refund.OutRefundNo)
	if err != nil {
		return nil, err
	}
	if prepared.ID != refund.ID {
		return nil, ErrPaymentStateConflict
	}
	return uc.executeRefund(ctx, method, current, prepared)
}

// Record exactly once, and count a deferral only after the pending row was
// successfully updated. Provider rejections terminate retries, while transport,
// preparation and settlement errors retain pending and advance its queue time.
func (uc *paymentUsecase) recordRefundFailure(ctx context.Context, refundID int64, result *PaymentRefundResult, cause error) string {
	definitive := result != nil && result.Rejection
	if err := uc.paymentRepo.RecordPaymentRefundError(ctx, refundID, cause.Error(), definitive); err != nil {
		uc.log.WithContext(ctx).Errorw("msg", "record payment refund error failed", "refund_id", refundID, "error", err)
		return "record_error"
	}
	if definitive {
		return "rejected"
	}
	return "deferred"
}
