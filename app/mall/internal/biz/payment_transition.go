package biz

// PaymentSuccessDecision describes fulfilment effects of a verified success.
// The repository supplies facts read under the order/payment locks and applies
// this decision in the same transaction that records the provider fact.
type PaymentSuccessDecision struct {
	MarkOrderPaid        bool
	ReconciliationReason string
	ReconciliationDetail string
}

func DecidePaymentSuccess(orderStatus string, anotherPaymentSucceeded bool) (PaymentSuccessDecision, error) {
	if anotherPaymentSucceeded {
		return PaymentSuccessDecision{ReconciliationReason: "duplicate_success", ReconciliationDetail: "another payment already completed this order"}, nil
	}
	switch orderStatus {
	case OrderStatusPendingPayment:
		return PaymentSuccessDecision{MarkOrderPaid: true}, nil
	case OrderStatusCancelling, OrderStatusCancelled:
		return PaymentSuccessDecision{ReconciliationReason: "late_success_after_cancel", ReconciliationDetail: "payment succeeded after order cancellation started"}, nil
	case OrderStatusPaid, OrderStatusShipped, OrderStatusCompleted:
		return PaymentSuccessDecision{}, nil
	default:
		return PaymentSuccessDecision{}, ErrPaymentStateConflict
	}
}

// RefundPurpose separates a reversal of fulfilment from returning excess money.
// The chosen purpose is persisted before contacting the provider and is reused
// after a crash, even if another payment's state subsequently changes.
type RefundPurpose string

const (
	RefundOrderCancel RefundPurpose = "order_cancel_refund"
	RefundDuplicate   RefundPurpose = "duplicate_payment_refund"
	RefundLate        RefundPurpose = "late_payment_refund"
)

func DecideRefundPurpose(orderStatus string, paidPaymentID, paymentID int64) (RefundPurpose, error) {
	if paymentID <= 0 {
		return "", ErrPaymentStateConflict
	}
	if paidPaymentID > 0 && paidPaymentID != paymentID {
		switch orderStatus {
		case OrderStatusPaid, OrderStatusShipped, OrderStatusCompleted, OrderStatusRefunded:
			return RefundDuplicate, nil
		}
	}
	if paidPaymentID == paymentID && orderStatus == OrderStatusPaid {
		return RefundOrderCancel, nil
	}
	if paidPaymentID == 0 && (orderStatus == OrderStatusCancelled || orderStatus == OrderStatusCancelling) {
		return RefundLate, nil
	}
	return "", ErrPaymentStateConflict
}

func ValidateRefundSettlement(purpose RefundPurpose, orderStatus string, paidPaymentID, paymentID int64) error {
	expected, err := DecideRefundPurpose(orderStatus, paidPaymentID, paymentID)
	if err != nil || expected != purpose {
		return ErrPaymentStateConflict
	}
	return nil
}
