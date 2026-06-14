package data

import (
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentRepo_CreatePayment_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)

	mockQ.EXPECT().
		CreatePaymentWithOutTradeNo(gomock.Any(), db.CreatePaymentWithOutTradeNoParams{
			OrderID:    10,
			UserID:     1,
			MerchantID: 2,
			Amount:     decimal.NewFromInt(9900),
			Status:     "pending",
			PayChannel: "wechat",
			OutTradeNo: pgtype.Text{String: "snow-1", Valid: true},
		}).
		Times(1).
		Return(db.Payment{
			ID:         100,
			OrderID:    10,
			UserID:     1,
			MerchantID: 2,
			Amount:     decimal.NewFromInt(9900),
			Status:     "pending",
			PayChannel: "wechat",
			OutTradeNo: pgtype.Text{String: "snow-1", Valid: true},
		}, nil)

	r := &PaymentRepo{q: mockQ}
	got, err := r.CreatePayment(context.Background(), biz.CreatePaymentArgs{
		OrderID:    10,
		UserID:     1,
		MerchantID: 2,
		Amount:     9900,
		PayChannel: "wechat",
		OutTradeNo: "snow-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(100), got.ID)
	assert.Equal(t, "snow-1", got.OutTradeNo)
	assert.Equal(t, "wechat", got.PayChannel)
}

func TestPaymentRepo_CreatePayment_DBUniqueConflict_MapsToErrPaymentConflict(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)

	pgErr := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_payments_active_out_trade_no_channel",
		Message:        "duplicate key value violates unique constraint",
	}
	mockQ.EXPECT().
		CreatePaymentWithOutTradeNo(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Payment{}, pgErr)

	r := &PaymentRepo{q: mockQ}
	_, err := r.CreatePayment(context.Background(), biz.CreatePaymentArgs{
		OrderID:    10,
		OutTradeNo: "snow-dup",
		PayChannel: "wechat",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, biz.ErrPaymentConflict),
		"expected biz.ErrPaymentConflict, got %v", err)
}

func TestPaymentRepo_CreatePayment_OtherDBError_PropagatedAsIs(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)

	pgErr := &pgconn.PgError{
		Code:    "23502",
		Message: "not_null_violation",
	}
	mockQ.EXPECT().
		CreatePaymentWithOutTradeNo(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Payment{}, pgErr)

	r := &PaymentRepo{q: mockQ}
	_, err := r.CreatePayment(context.Background(), biz.CreatePaymentArgs{
		OrderID:    10,
		OutTradeNo: "snow-1",
		PayChannel: "wechat",
	})
	require.Error(t, err)
	assert.False(t, errors.Is(err, biz.ErrPaymentConflict),
		"non-23505 errors must NOT be remapped to ErrPaymentConflict")
}

func TestPaymentRepo_GetActivePaymentByOrderChannel(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)

	mockQ.EXPECT().
		GetActivePaymentByOrderChannel(gomock.Any(), db.GetActivePaymentByOrderChannelParams{
			OrderID:    10,
			PayChannel: "wechat",
		}).
		Times(1).
		Return(db.Payment{
			ID:         100,
			OrderID:    10,
			PayChannel: "wechat",
			Status:     "pending",
			OutTradeNo: pgtype.Text{String: "snow-1", Valid: true},
		}, nil)

	r := &PaymentRepo{q: mockQ}
	got, err := r.GetActivePaymentByOrderChannel(context.Background(), 10, "wechat")
	require.NoError(t, err)
	assert.Equal(t, int64(100), got.ID)
	assert.Equal(t, "wechat", got.PayChannel)
	assert.Equal(t, "snow-1", got.OutTradeNo)
}
