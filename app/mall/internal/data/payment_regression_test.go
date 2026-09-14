package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type paymentCommitHookTx struct {
	q     db.Querier
	after func()
}

func (t paymentCommitHookTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(WithQuerier(ctx, t.q, nil)); err != nil {
		return err
	}
	t.after()
	return nil
}

func TestReviewPaymentMutationDoesNotPublishOldSnapshot(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	d := newTestData(t, q, mr)
	ctx := context.Background()
	pending := statePayment(biz.PaymentStatusPending)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), pending.OrderID).Return(db.Order{ID: pending.OrderID, UserID: pending.UserID, TotalAmountMinor: pending.AmountMinor, Currency: pending.Currency, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), pending.OrderID).Return([]db.Payment{pending}, nil)
	var r *PaymentRepo
	r = NewPaymentRepo(d, paymentCommitHookTx{q: q, after: func() {
		// The caller has fetched pending and committed. Before it resumes, another
		// transaction records success and invalidates all payment cache keys.
		r.invalidatePayment(ctx, statePayment(biz.PaymentStatusSuccess))
	}}, log.DefaultLogger)
	old, e := r.CreatePayment(ctx, biz.CreatePaymentArgs{OrderID: pending.OrderID, UserID: pending.UserID, Amount: pending.AmountMinor, Currency: pending.Currency, Method: pending.PayChannel})
	require.NoError(t, e)
	require.Equal(t, biz.PaymentStatusPending, old.Status)
	q.EXPECT().GetPayment(gomock.Any(), pending.ID).Return(statePayment(biz.PaymentStatusSuccess), nil)
	p, e := r.GetPayment(ctx, pending.ID)
	require.NoError(t, e)
	require.Equal(t, biz.PaymentStatusSuccess, p.Status)
}

func TestReviewRefundedClosed(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "refund_mismatch"}[mismatch], func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			p := statePayment(biz.PaymentStatusRefunded)
			p.ThirdPartyTxID = pgtype.Text{String: "tx_1", Valid: true}
			q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(p, nil).AnyTimes()
			q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2}, nil).AnyTimes()
			q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{p}, nil).AnyTimes()
			refund := db.OrderRefund{OrderID: 2, UserID: 3, TotalAmountMinor: 10000, RefundAmountMinor: 10000, Currency: "CNY", Status: biz.PaymentRefundStatusSuccess}
			if mismatch {
				refund.RefundAmountMinor = 1
			}
			q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), gomock.Any()).Return(refund, nil).AnyTimes()
			r := NewPaymentRepo(&Data{q: q}, testTxManager{q: q}, log.DefaultLogger)
			result := stateResult(10000)
			result.TradeState = biz.TradeStateClosed
			for range 2 {
				e := r.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1}, result)
				if mismatch {
					require.Error(t, e)
				} else {
					require.NoError(t, e)
				}
			}
		})
	}
}
