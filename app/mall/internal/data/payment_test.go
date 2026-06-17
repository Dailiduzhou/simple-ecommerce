package data

import (
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
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
	mr := miniredis.RunT(t)

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

	d := newTestData(t, mockQ, mr)
	r := NewPaymentRepo(d, nil, log.DefaultLogger)
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
	mr := miniredis.RunT(t)

	pgErr := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_payments_active_out_trade_no_channel",
		Message:        "duplicate key value violates unique constraint",
	}
	mockQ.EXPECT().
		CreatePaymentWithOutTradeNo(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Payment{}, pgErr)

	d := newTestData(t, mockQ, mr)
	r := NewPaymentRepo(d, nil, log.DefaultLogger)
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
	mr := miniredis.RunT(t)

	pgErr := &pgconn.PgError{
		Code:    "23502",
		Message: "not_null_violation",
	}
	mockQ.EXPECT().
		CreatePaymentWithOutTradeNo(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Payment{}, pgErr)

	d := newTestData(t, mockQ, mr)
	r := NewPaymentRepo(d, nil, log.DefaultLogger)
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
	mr := miniredis.RunT(t)

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

	d := newTestData(t, mockQ, mr)
	r := NewPaymentRepo(d, nil, log.DefaultLogger)
	got, err := r.GetActivePaymentByOrderChannel(context.Background(), 10, "wechat")
	require.NoError(t, err)
	assert.Equal(t, int64(100), got.ID)
	assert.Equal(t, "wechat", got.PayChannel)
	assert.Equal(t, "snow-1", got.OutTradeNo)
}

func TestPaymentRepo_ClosePayment(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	r := NewPaymentRepo(d, nil, log.DefaultLogger)

	// Pre-populate cache entries
	r.setCache(context.Background(), "payment:200", &biz.PaymentDO{ID: 200, OrderID: 20, PayChannel: "wechat", OutTradeNo: "snow-close"})
	r.setCache(context.Background(), "payment:order:20", &biz.PaymentDO{ID: 200, OrderID: 20})
	r.setCache(context.Background(), "payment:order:20:active:wechat", &biz.PaymentDO{ID: 200, OrderID: 20, PayChannel: "wechat"})
	r.setCache(context.Background(), "payment:out_trade_no:snow-close", &biz.PaymentDO{ID: 200, OrderID: 20, OutTradeNo: "snow-close"})

	mockPayment := db.Payment{
		ID:         200,
		OrderID:    20,
		PayChannel: "wechat",
		OutTradeNo: pgtype.Text{String: "snow-close", Valid: true},
	}

	// 1. Read payment for cache key resolution
	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(200)).
		Times(1).
		Return(mockPayment, nil)

	// 2. Update payment status
	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(200)).
		Times(1).
		Return(nil)

	// 3. Cancel order
	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(20)).
		Times(1).
		Return(nil)

	err := r.ClosePayment(context.Background(), 200, 20)
	require.NoError(t, err)

	// Verify all cache keys cleared
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "payment:200").Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "payment:order:20").Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "payment:order:20:active:wechat").Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "payment:out_trade_no:snow-close").Val())
}

func TestPaymentRepo_ClosePayment_FallbackOrderID(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	r := NewPaymentRepo(d, nil, log.DefaultLogger)

	mockPayment := db.Payment{
		ID:         300,
		OrderID:    30,
		PayChannel: "alipay",
		OutTradeNo: pgtype.Text{String: "snow-close-ali", Valid: true},
	}

	mockQ.EXPECT().
		GetPayment(gomock.Any(), int64(300)).
		Times(1).
		Return(mockPayment, nil)

	mockQ.EXPECT().
		UpdatePaymentFailed(gomock.Any(), int64(300)).
		Times(1).
		Return(nil)

	mockQ.EXPECT().
		CancelOrder(gomock.Any(), int64(30)). // from payment.OrderID fallback
		Times(1).
		Return(nil)

	err := r.ClosePayment(context.Background(), 300, 0)
	require.NoError(t, err)
}
