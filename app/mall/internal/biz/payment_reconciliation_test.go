package biz

import (
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReconciliationResolutionRequiresConsistentSettledFacts(t *testing.T) {
	for _, tc := range []struct {
		name, status, order string
		paid                int64
		purpose             RefundPurpose
		want                bool
	}{
		{"primary paid", PaymentStatusSuccess, OrderStatusPaid, 1, "", true},
		{"primary shipped", PaymentStatusSuccess, OrderStatusShipped, 1, "", true},
		{"duplicate money", PaymentStatusSuccess, OrderStatusPaid, 2, "", false},
		{"late money", PaymentStatusSuccess, OrderStatusCancelled, 0, "", false},
		{"unsettled", PaymentStatusPending, OrderStatusPendingPayment, 0, "", false},
		{"ordinary refund", PaymentStatusRefunded, OrderStatusRefunded, 1, RefundOrderCancel, true},
		{"late refund", PaymentStatusRefunded, OrderStatusCancelled, 0, RefundLate, true},
		{"duplicate refund", PaymentStatusRefunded, OrderStatusCompleted, 2, RefundDuplicate, true},
		{"duplicate refund after primary refund", PaymentStatusRefunded, OrderStatusRefunded, 2, RefundDuplicate, true},
		{"refund mismatch", PaymentStatusRefunded, OrderStatusPaid, 1, RefundOrderCancel, false},
		{"closed cancelled", PaymentStatusClosed, OrderStatusCancelled, 0, "", true},
		{"closed payable", PaymentStatusClosed, OrderStatusPendingPayment, 0, "", true},
		{"closed sibling after primary refund", PaymentStatusClosed, OrderStatusRefunded, 2, "", true},
		{"failed sibling after primary refund", PaymentStatusFailed, OrderStatusRefunded, 2, "", true},
		{"closed pending with primary", PaymentStatusClosed, OrderStatusPendingPayment, 1, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PaymentDO{ID: 1, OrderID: 7, UserID: 42, Amount: 100, Currency: "CNY", Status: tc.status, ThirdPartyTxID: "provider-tx"}
			o := Order{ID: 7, UserID: 42, TotalAmount: 100, Currency: "CNY", Status: tc.order, PaidPaymentID: tc.paid}
			var refund *PaymentRefund
			if tc.purpose != "" {
				refund = &PaymentRefund{PaymentID: 1, OrderID: 7, UserID: 42, TotalAmount: 100, RefundAmount: 100, Currency: "CNY", Purpose: tc.purpose, Status: PaymentRefundStatusSuccess}
			}
			require.Equal(t, tc.want, CanResolveReconciliation(p, o, refund))
			p.Amount++
			require.False(t, CanResolveReconciliation(p, o, refund), "mismatched financial binding cannot be acknowledged")
		})
	}
}

func TestReconciliationCommandAuthorizationAndEvidence(t *testing.T) {
	in := ReconciliationInput{PaymentID: 1, ExpectedVersion: 1, Action: ReconciliationActionResolve, IdempotencyKey: "resolve-001", Reason: "verified", Evidence: "provider statement 123"}
	require.EqualValues(t, 401, errors.FromError(in.Validate(Actor{})).Code)
	require.EqualValues(t, 403, errors.FromError(in.Validate(Actor{ID: 42})).Code)
	require.NoError(t, in.Validate(Actor{ID: 42, Admin: true}))
	in.Evidence = " "
	require.EqualValues(t, 400, errors.FromError(in.Validate(Actor{ID: 42, Admin: true})).Code)
}
