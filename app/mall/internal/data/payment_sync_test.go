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

// txRecorder is a minimal biz.TxManager used to assert that InTx is called
// and to inject a mock Querier into the context the closure runs in.
type txRecorder struct {
	called bool
	q      db.Querier
	fnErr  error
}

func (t *txRecorder) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	t.called = true
	return fn(WithQuerier(ctx, t.q, nil))
}

func newTestPaymentSyncRepo(ctrl *gomock.Controller, q db.Querier) (*PaymentSyncRepo, *txRecorder) {
	tx := &txRecorder{q: q}
	return NewPaymentSyncRepo(tx), tx
}

func TestPaymentSyncRepo_ApplyPayQuery_Success_WithOrderID(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, tx := newTestPaymentSyncRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentSuccess(gomock.Any(), db.UpdatePaymentSuccessParams{
			ID: 100,
			ThirdPartyTxID: pgtype.Text{String: "tx-abc", Valid: true},
		}).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 555}, nil)
	mockQ.EXPECT().
		CompleteOrder(gomock.Any(), int64(555)).
		Times(1).
		Return(nil)

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

func TestPaymentSyncRepo_ApplyPayQuery_Success_OrderIDFromPayment(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentSuccess(gomock.Any(), db.UpdatePaymentSuccessParams{
			ID: 100,
			ThirdPartyTxID: pgtype.Text{String: "", Valid: false},
		}).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 777}, nil)
	mockQ.EXPECT().
		CompleteOrder(gomock.Any(), int64(777)).
		Times(1).
		Return(nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{
		TransactionID: "",
		TradeState:    biz.TradeStateSuccess,
	})
	require.NoError(t, err)
}

func TestPaymentSyncRepo_ApplyPayQuery_Success_NoOrderID_NoCancel(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

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

func TestPaymentSyncRepo_ApplyPayQuery_Refund(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentRefunded(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{
		TradeState: biz.TradeStateRefund,
	})
	require.NoError(t, err)
}

func TestPaymentSyncRepo_ApplyPayQuery_FailedWithOrderID(t *testing.T) {
	states := []biz.TradeState{biz.TradeStateClosed, biz.TradeStateRevoked, biz.TradeStatePayError}
	for _, st := range states {
		st := st
		t.Run(st.String(), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockQ := mockdb.NewMockQuerier(ctrl)
			repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

			mockQ.EXPECT().
				UpdatePaymentFailed(gomock.Any(), int64(100)).
				Times(1).
				Return(nil)
			mockQ.EXPECT().
				CancelOrder(gomock.Any(), int64(888)).
				Times(1).
				Return(nil)

			err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
				PaymentID: 100,
				OrderID:   888,
			}, &biz.PaymentQueryResult{TradeState: st})
			require.NoError(t, err)
		})
	}
}

func TestPaymentSyncRepo_ApplyPayQuery_FailedOrderIDFromDB(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(100)).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 999}, nil)
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(999)).
		Times(1).
		Return(nil)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{TradeState: biz.TradeStateClosed})
	require.NoError(t, err)
}

func TestPaymentSyncRepo_ApplyPayQuery_NonTerminalState(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

	err := repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
	}, &biz.PaymentQueryResult{TradeState: biz.TradeStateNotPay})
	require.Error(t, err)
}

func TestPaymentSyncRepo_ApplyPayQuery_UpdateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

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

func TestPaymentSyncRepo_MarkPayExpired(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(100)).
		Times(1).
		Return(db.Payment{ID: 100, OrderID: 444}, nil)
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(444)).
		Times(1).
		Return(nil)

	err := repo.MarkPayExpired(context.Background(), biz.CheckPayArgs{PaymentID: 100})
	require.NoError(t, err)
}

func TestPaymentSyncRepo_MarkPayExpired_WithOrderIDSkipsGetPayment(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	repo, _ := newTestPaymentSyncRepo(ctrl, mockQ)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(100)).
		Times(1).
		Return(nil)
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(42)).
		Times(1).
		Return(nil)

	err := repo.MarkPayExpired(context.Background(), biz.CheckPayArgs{
		PaymentID: 100,
		OrderID:   42,
	})
	require.NoError(t, err)
}
