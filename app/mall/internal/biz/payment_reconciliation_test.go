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
		expired, want       bool
	}{
		{"primary paid", PaymentStatusSuccess, OrderStatusPaid, 1, "", false, true},
		{"primary shipped", PaymentStatusSuccess, OrderStatusShipped, 1, "", false, true},
		{"duplicate money", PaymentStatusSuccess, OrderStatusPaid, 2, "", false, false},
		{"late money", PaymentStatusSuccess, OrderStatusCancelled, 0, "", true, false},
		{"unsettled", PaymentStatusPending, OrderStatusPendingPayment, 0, "", false, false},
		{"ordinary refund", PaymentStatusRefunded, OrderStatusRefunded, 1, RefundOrderCancel, true, true},
		{"late refund", PaymentStatusRefunded, OrderStatusCancelled, 0, RefundLate, true, true},
		{"duplicate refund", PaymentStatusRefunded, OrderStatusCompleted, 2, RefundDuplicate, true, true},
		{"refund mismatch", PaymentStatusRefunded, OrderStatusPaid, 1, RefundOrderCancel, true, false},
		{"closed cancelled", PaymentStatusClosed, OrderStatusCancelled, 0, "", true, true},
		{"closed payable", PaymentStatusClosed, OrderStatusPendingPayment, 0, "", false, true},
		{"closed overdue", PaymentStatusClosed, OrderStatusPendingPayment, 0, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &PaymentDO{ID: 1, OrderID: 7, UserID: 42, Amount: 100, Currency: "CNY", Status: tc.status, ThirdPartyTxID: "provider-tx"}
			o := Order{ID: 7, UserID: 42, TotalAmount: 100, Currency: "CNY", Status: tc.order, PaidPaymentID: tc.paid}
			var refund *PaymentRefund
			if tc.purpose != "" {
				refund = &PaymentRefund{PaymentID: 1, OrderID: 7, UserID: 42, TotalAmount: 100, RefundAmount: 100, Currency: "CNY", Purpose: tc.purpose, Status: PaymentRefundStatusSuccess}
			}
			require.Equal(t, tc.want, CanResolveReconciliation(p, o, refund, tc.expired))
			p.Amount++
			require.False(t, CanResolveReconciliation(p, o, refund, tc.expired), "mismatched financial binding cannot be acknowledged")
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
