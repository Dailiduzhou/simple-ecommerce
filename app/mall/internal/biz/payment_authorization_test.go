package biz

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestPaymentReadAuthorization(t *testing.T) {
	payment := &PaymentDO{ID: 1, UserID: 42, MerchantID: 7}

	require.NoError(t, authorizePayment(Actor{ID: 42, Role: RoleUser}, payment), "buyer")
	require.NoError(t, authorizePayment(Actor{ID: 7, Role: RoleMerchant}, payment), "collecting merchant")
	require.NoError(t, authorizePayment(Actor{ID: 1, Role: RoleAdmin}, payment), "administrator")
	require.ErrorIs(t, authorizePayment(Actor{ID: 8, Role: RoleMerchant}, payment), ErrPaymentNotFound, "other merchant")
	require.ErrorIs(t, authorizePayment(Actor{ID: 9, Role: RoleUser}, payment), ErrPaymentNotFound, "other buyer")
	require.Equal(t, int32(401), errors.FromError(authorizePayment(Actor{ID: 42}, payment)).Code)
	require.ErrorIs(t, authorizePayment(Actor{ID: 42, Role: RoleUser}, nil), ErrPaymentNotFound)
}

func TestRefundPaymentRequiresOwningMerchant(t *testing.T) {
	payment := &PaymentDO{ID: 1, UserID: 42, MerchantID: 7, Status: PaymentStatusSuccess, Method: "alipay:wap"}
	repo := &paymentTestRepo{payment: payment}
	uc := NewPaymentUsecase(nil, repo, nil, nil, nil, nil, paymentTestID{}, log.DefaultLogger)

	_, err := uc.RefundPayment(context.Background(), Actor{ID: 8, Role: RoleMerchant}, payment.ID)
	require.Equal(t, int32(403), errors.FromError(err).Code)
	require.False(t, repo.refundApplied, "an unauthorized refund must not reach the gateway")

	_, err = uc.RefundPayment(context.Background(), Actor{ID: 42, Role: RoleUser}, payment.ID)
	require.Equal(t, int32(403), errors.FromError(err).Code)
	require.False(t, repo.refundApplied)
}
