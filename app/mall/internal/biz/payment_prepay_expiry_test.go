package biz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type expiryPrepayRepo struct {
	*paymentTestRepo
	cleanedToken string
}

func (r *expiryPrepayRepo) FinalizePaymentPrepay(context.Context, int64, string, PaymentAction) (*PaymentDO, error) {
	return nil, ErrPaymentStateConflict
}
func (r *expiryPrepayRepo) RecordPaymentPrepayError(_ context.Context, _ int64, token, _ string) error {
	r.cleanedToken = token
	return nil
}

type expiryPrepayGateway struct {
	*paymentTestGateway
	repo *expiryPrepayRepo
	fail bool
}

func (g *expiryPrepayGateway) Prepay(context.Context, PaymentPrepayRequest) (*PaymentPrepayResult, error) {
	// Expiry fences creating while local signing is in progress.
	g.repo.payment.Status = PaymentStatusClosePending
	if g.fail {
		return nil, errors.New("signing failed")
	}
	return &PaymentPrepayResult{Action: PaymentAction{Type: "invoke", Payload: []byte(`{"payload":"signed"}`)}}, nil
}

func TestPrepayExpiryCleansLeaseWithoutReturningAction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "finalize conflict"
		if fail {
			name = "signing failure"
		}
		t.Run(name, func(t *testing.T) {
			repo := &expiryPrepayRepo{paymentTestRepo: &paymentTestRepo{}}
			gateway := &expiryPrepayGateway{paymentTestGateway: &paymentTestGateway{}, repo: repo, fail: fail}
			orders := &orderTestRepo{order: Order{ID: 5, UserID: 42, TotalAmount: 100, Currency: "CNY", Status: OrderStatusPendingPayment, OutTradeNo: "order_5", ExpiresAt: time.Now().Add(time.Minute)}}
			uc := NewPaymentUsecase(gateway, repo, nil, orders, nil, &paymentTestTx{}, paymentTestID{}, log.DefaultLogger)
			result, err := uc.PrepayForOrder(context.Background(), PrepayForOrderArgs{OrderNo: "order_5", UserID: 42, Method: PaymentMethod{Provider: "alipay", Product: "app"}})
			require.Error(t, err)
			require.Nil(t, result)
			require.NotEmpty(t, repo.cleanedToken)
			require.Equal(t, repo.payment.PrepayLeaseToken, repo.cleanedToken)
			require.Equal(t, PaymentStatusClosePending, repo.payment.Status)
		})
	}
}
