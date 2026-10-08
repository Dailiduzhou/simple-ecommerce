package biz

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaymentSuccessDecisionMatrix(t *testing.T) {
	for _, status := range []string{OrderStatusPendingPayment, OrderStatusPaid, OrderStatusShipped, OrderStatusCompleted, OrderStatusCancelling, OrderStatusCancelled, OrderStatusRefunded, "unknown"} {
		for _, sibling := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sibling=%t", status, sibling), func(t *testing.T) {
				d, err := DecidePaymentSuccess(status, sibling)
				switch {
				case sibling:
					require.NoError(t, err)
					require.False(t, d.MarkOrderPaid)
					require.Equal(t, "duplicate_success", d.ReconciliationReason)
				case status == OrderStatusCancelled || status == OrderStatusCancelling:
					require.NoError(t, err)
					require.False(t, d.MarkOrderPaid)
					require.Equal(t, "late_success_after_cancel", d.ReconciliationReason)
				case status == OrderStatusRefunded || status == "unknown":
					require.ErrorIs(t, err, ErrPaymentStateConflict)
				default:
					require.NoError(t, err)
					require.Equal(t, status == OrderStatusPendingPayment, d.MarkOrderPaid)
					require.Empty(t, d.ReconciliationReason)
				}
			})
		}
	}
}

func TestRefundPurposeMatrix(t *testing.T) {
	for _, tc := range []struct {
		status string
		paidID int64
		want   RefundPurpose
	}{
		{OrderStatusPaid, 1, RefundOrderCancel},
		{OrderStatusPaid, 2, RefundDuplicate},
		{OrderStatusShipped, 2, RefundDuplicate},
		{OrderStatusCompleted, 2, RefundDuplicate},
		{OrderStatusRefunded, 2, RefundDuplicate},
		{OrderStatusCancelled, 0, RefundLate},
		{OrderStatusCancelling, 0, RefundLate},
		{OrderStatusShipped, 1, ""},
		{OrderStatusCompleted, 1, ""},
		{OrderStatusRefunded, 1, ""},
		{OrderStatusPendingPayment, 0, ""},
		{OrderStatusPaid, 0, ""},
		{OrderStatusCancelled, 1, ""},
	} {
		t.Run(tc.status+"/"+string(tc.want), func(t *testing.T) {
			got, err := DecideRefundPurpose(tc.status, tc.paidID, 1)
			if tc.want == "" {
				require.ErrorIs(t, err, ErrPaymentStateConflict)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
				require.NoError(t, ValidateRefundSettlement(got, tc.status, tc.paidID, 1))
			}
		})
	}
	require.ErrorIs(t, ValidateRefundSettlement(RefundOrderCancel, OrderStatusPaid, 2, 1), ErrPaymentStateConflict)
	require.ErrorIs(t, ValidateRefundSettlement(RefundDuplicate, OrderStatusPaid, 1, 1), ErrPaymentStateConflict)
}
