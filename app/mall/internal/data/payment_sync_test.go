package data

import (
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type txRecorder struct {
	called bool
	q      db.Querier
}

func (t *txRecorder) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	t.called = true
	ctx = WithQuerier(ctx, t.q, nil)
	ctx = context.WithValue(ctx, txStateKey{}, &txState{})
	return fn(ctx)
}

func newTestPaymentRepo(ctrl *gomock.Controller, q db.Querier) (*PaymentRepo, *txRecorder) {
	tx := &txRecorder{q: q}
	return &PaymentRepo{tx: tx}, tx
}

func TestPaymentRepo_ApplyPayQuery_Success_WithOrderID(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, tx := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentSuccess(gomock.Any(), db.UpdatePaymentSuccessParams{
			ID:             100,
			ThirdPartyTxID: pgtype.Text{String: "tx-abc", Valid: true},
		}).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 555}, nil)
	mockQ.EXPECT().
		CompleteOrder(gomock.Any(), int64(555)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetOrder(gomock.Any(), int64(555)).
		Times(1).
		Return(db.Order{ID: 555, UserID: 7, OutTradeNo: pgtype.Text{String: "order-555", Valid: true}}, nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
		OrderID:   555,
	}, &biz.PaymentQueryResult{
		TransactionID: "tx-abc",
		TradeState:    biz.TradeStateSuccess,
	})
	require.NoError(t, err)
	assert.True(t, tx.called)
}

func TestPaymentRepo_ApplyPayQuery_Success_OrderIDFromPayment(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentSuccess(gomock.Any(), db.UpdatePaymentSuccessParams{
			ID:             100,
			ThirdPartyTxID: pgtype.Text{String: "", Valid: false},
		}).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 777}, nil)
	mockQ.EXPECT().
		CompleteOrder(gomock.Any(), int64(777)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetOrder(gomock.Any(), int64(777)).
		Times(1).
		Return(db.Order{ID: 777, UserID: 8, OutTradeNo: pgtype.Text{String: "order-777", Valid: true}}, nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{
		TransactionID: "",
		TradeState:    biz.TradeStateSuccess,
	})
	require.NoError(t, err)
}

func TestPaymentRepo_ApplyPayQuery_Success_NoOrderID_NoCancel(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentSuccess(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 0}, nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{
		TradeState: biz.TradeStateSuccess,
	})
	require.NoError(t, err)
}

func TestPaymentRepo_ApplyPayQuery_Refund(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentRefunded(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(100)).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 555, PayChannel: "wechat", OutTradeNo: pgtype.Text{String: "pay-100", Valid: true}}, nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{
		TradeState: biz.TradeStateRefund,
	})
	require.NoError(t, err)
}

func TestPaymentRepo_ApplyPayQuery_FailedWithOrderID(t *testing.T) {
	states := []biz.TradeState{biz.TradeStateClosed, biz.TradeStateRevoked, biz.TradeStatePayError}
	for _, st := range states {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockQ := mockdb.NewMockQuerier(ctrl)
			repo, _ := newTestPaymentRepo(ctrl, mockQ)

			mockQ.EXPECT().
				UpdatePaymentFailed(gomock.Any(), int64(100)).
				Times(1).
				Return(nil)
			mockQ.EXPECT().
				GetPayment(gomock.Any(), int64(100)).
				Times(1).
				Return(db.Payment{ID: 100, OrderID: 888, PayChannel: "wechat", OutTradeNo: pgtype.Text{String: "pay-100", Valid: true}}, nil)
			mockQ.EXPECT().
				CancelOrder(gomock.Any(), int64(888)).
				Times(1).
				Return(nil)
			mockQ.EXPECT().
				GetOrder(gomock.Any(), int64(888)).
				Times(1).
				Return(db.Order{ID: 888, UserID: 9, OutTradeNo: pgtype.Text{String: "order-888", Valid: true}}, nil)

			err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
				PaymentID: 100,
				OrderID:   888,
			}, &biz.PaymentQueryResult{TradeState: st})
			require.NoError(t, err)
		})
	}
}

func TestPaymentRepo_ApplyPayQuery_FailedOrderIDFromDB(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(100)).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 999, PayChannel: "wechat", OutTradeNo: pgtype.Text{String: "pay-100", Valid: true}}, nil)
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(999)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetOrder(gomock.Any(), int64(999)).
		Times(1).
		Return(db.Order{ID: 999, UserID: 10, OutTradeNo: pgtype.Text{String: "order-999", Valid: true}}, nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{TradeState: biz.TradeStateClosed})
	require.NoError(t, err)
}

func TestPaymentRepo_ApplyPayQuery_NonTerminalState(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{TradeState: biz.TradeStateNotPay})
	require.Error(t, err)
}

func TestPaymentRepo_ApplyPayQuery_UpdateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	dbErr := errors.New("db boom")
	mockQ.EXPECT().
		UpdatePaymentSuccess(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Payment{}, dbErr)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{TradeState: biz.TradeStateSuccess})
	require.ErrorIs(t, err, dbErr)
}

func TestPaymentRepo_MarkPayExpired(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(100)).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 444, PayChannel: "wechat", OutTradeNo: pgtype.Text{String: "pay-100", Valid: true}}, nil)
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(444)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetOrder(gomock.Any(), int64(444)).
		Times(1).
		Return(db.Order{ID: 444, UserID: 11, OutTradeNo: pgtype.Text{String: "order-444", Valid: true}}, nil)

	err := repo.MarkPayExpired(context.Background(), biz.CheckPayArgs{PaymentID: 100})
	require.NoError(t, err)
}

func TestPaymentRepo_MarkPayExpired_WithOrderIDStillLoadsPaymentForCacheInvalidation(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(100)).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 42, PayChannel: "wechat", OutTradeNo: pgtype.Text{String: "pay-100", Valid: true}}, nil)
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(42)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetOrder(gomock.Any(), int64(42)).
		Times(1).
		Return(db.Order{ID: 42, UserID: 12, OutTradeNo: pgtype.Text{String: "order-42", Valid: true}}, nil)

	err := repo.MarkPayExpired(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
		OrderID:   42,
	})
	require.NoError(t, err)
}
