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

func statePayment(status string) db.Payment {
	return db.Payment{ID: 1, OrderID: 2, UserID: 3, AmountMinor: 10000, Currency: "CNY", Status: status, PayChannel: "wechat:native", OutTradeNo: pgtype.Text{String: "pay_1", Valid: true}}
}
func stateResult(amount int64) *biz.PaymentQueryResult {
	return &biz.PaymentQueryResult{Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1", TransactionID: "tx_1", TradeState: biz.TradeStateSuccess, Amount: amount, Currency: "CNY"}
}

func TestApplyPayQuery_AmountMismatchPersistsReconciliationWithoutSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	reconciled := payment
	reconciled.Status = biz.PaymentStatusReconcileRequired
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().MarkPaymentReconcileRequired(gomock.Any(), int64(1)).Return(reconciled, nil)
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Contains(t, args.LastError, "amount")
		return db.PaymentReconciliationFailure{}, nil
	})
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(1)))
}

func TestApplyPayQuery_SuccessUsesPaymentAndOrderCAS(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	succeeded := payment
	succeeded.Status = biz.PaymentStatusSuccess
	succeeded.ThirdPartyTxID = pgtype.Text{String: "tx_1", Valid: true}
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().MarkPaymentSuccess(gomock.Any(), db.MarkPaymentSuccessParams{ID: 1, ThirdPartyTxID: pgtype.Text{String: "tx_1", Valid: true}}).Return(succeeded, nil)
	q.EXPECT().MarkOrderPaid(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(10000)))
}

func TestApplyPayQuery_LateSuccessMovesClosedPaymentToReconciliation(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusClosed)
	reconciled := payment
	reconciled.Status = biz.PaymentStatusReconcileRequired
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusCancelled}, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().MarkPaymentReconcileRequired(gomock.Any(), int64(1)).Return(reconciled, nil)
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Contains(t, args.LastError, "late success")
		return db.PaymentReconciliationFailure{}, nil
	})
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusCancelled}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(10000)))
}
