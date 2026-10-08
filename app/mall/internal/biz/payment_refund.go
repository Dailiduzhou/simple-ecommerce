package biz

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
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
	return uc.executeRefund(ctx, method, payment, refund)
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
		// Only an explicit provider business rejection is definitive. Transient
		// transport or system errors keep the record pending so the
		// reconciliation worker can retry it with the same OutRefundNo.
		definitive := result != nil && result.Rejection
		if recordErr := uc.paymentRepo.RecordPaymentRefundError(ctx, refund.ID, err.Error(), definitive); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record payment refund error failed", "payment_id", payment.ID, "refund_id", refund.ID, "error", recordErr)
		}
		return result, err
	}
	if result == nil || !result.Success {
		err = errors.New(502, "PAYMENT_REFUND_FAILED", "payment provider did not confirm refund")
		if recordErr := uc.paymentRepo.RecordPaymentRefundError(ctx, refund.ID, err.Error(), false); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record payment refund rejection failed", "payment_id", payment.ID, "refund_id", refund.ID, "error", recordErr)
		}
		return result, err
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
		if err := uc.reconcileRefund(ctx, refund); err != nil {
			uc.log.WithContext(ctx).Errorw("msg", "reconcile pending refund failed", "payment_id", refund.PaymentID, "refund_id", refund.ID, "out_refund_no", refund.OutRefundNo, "error", err)
			continue
		}
		settled++
	}
	return settled, nil
}

func (uc *paymentUsecase) reconcileRefund(ctx context.Context, refund PaymentRefund) error {
	payment, err := uc.paymentRepo.GetPayment(ctx, refund.PaymentID)
	if err != nil {
		return err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return err
	}
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return err
	}
	if !capabilities.SupportsRefund {
		return fmt.Errorf("provider %s does not support refund", method.Provider)
	}
	// Re-preparing under the payment lock re-validates amounts and state, and
	// returns the existing refund record instead of creating a new one.
	current, prepared, err := uc.paymentRepo.PreparePaymentRefund(ctx, refund.PaymentID, refund.OutRefundNo)
	if err != nil {
		return err
	}
	if prepared.ID != refund.ID {
		return ErrPaymentStateConflict
	}
	_, err = uc.executeRefund(ctx, method, current, prepared)
	return err
}
