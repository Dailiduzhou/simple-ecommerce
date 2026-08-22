package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

func refundTestData(q db.Querier) *Data {
	return &Data{
		q: q,
		rdb: redis.NewClient(&redis.Options{
			Addr:         "127.0.0.1:1",
			MaxRetries:   -1,
			DialTimeout:  1,
			ReadTimeout:  1,
			WriteTimeout: 1,
		}),
		sg: &singleflight.Group{},
	}
}

func TestPreparePaymentRefundCreatesFullRefundUnderPaymentLock(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	payment := statePayment(biz.PaymentStatusSuccess)
	payment.PayChannel = "alipay:wap"
	refund := db.OrderRefund{
		ID: 11, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
		OrderID: payment.OrderID, UserID: payment.UserID, OutRefundNo: "refund_1",
		TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
		Currency: payment.Currency, Status: biz.PaymentRefundStatusPending,
	}
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), pgtype.Int8{Int64: payment.ID, Valid: true}).Return(db.OrderRefund{}, pgx.ErrNoRows)
	q.EXPECT().CreateOrderRefund(gomock.Any(), db.CreateOrderRefundParams{
		PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
		OrderID:   payment.OrderID, UserID: payment.UserID, OutRefundNo: "refund_1",
		TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
		Currency: payment.Currency,
	}).Return(refund, nil)

	d := refundTestData(q)
	t.Cleanup(func() { _ = d.rdb.Close() })
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	gotPayment, gotRefund, err := repo.PreparePaymentRefund(context.Background(), payment.ID, "refund_1")
	require.NoError(t, err)
	require.Equal(t, payment.ID, gotPayment.ID)
	require.Equal(t, payment.AmountMinor, gotRefund.RefundAmount)
	require.Equal(t, "refund_1", gotRefund.OutRefundNo)
}

func TestPreparePaymentRefundReusesSuccessfulRefund(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	payment := statePayment(biz.PaymentStatusRefunded)
	refund := db.OrderRefund{
		ID: 11, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
		OrderID: payment.OrderID, UserID: payment.UserID, OutRefundNo: "refund_existing",
		TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
		Currency: payment.Currency, Status: biz.PaymentRefundStatusSuccess,
	}
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), pgtype.Int8{Int64: payment.ID, Valid: true}).Return(refund, nil)

	d := refundTestData(q)
	t.Cleanup(func() { _ = d.rdb.Close() })
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	_, gotRefund, err := repo.PreparePaymentRefund(context.Background(), payment.ID, "new_refund")
	require.NoError(t, err)
	require.Equal(t, "refund_existing", gotRefund.OutRefundNo)
}

func TestApplyPaymentRefundUpdatesRefundAndPaymentAtomically(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	payment := statePayment(biz.PaymentStatusSuccess)
	payment.PayChannel = "alipay:wap"
	refund := db.OrderRefund{
		ID: 11, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
		OrderID: payment.OrderID, UserID: payment.UserID, OutRefundNo: "refund_1",
		TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
		Currency: payment.Currency, Status: biz.PaymentRefundStatusPending,
	}
	refundedPayment := payment
	refundedPayment.Status = biz.PaymentStatusRefunded
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), pgtype.Int8{Int64: payment.ID, Valid: true}).Return(refund, nil)
	q.EXPECT().MarkOrderRefundSuccess(gomock.Any(), db.MarkOrderRefundSuccessParams{
		ID: refund.ID, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
	}).Return(refund, nil)
	q.EXPECT().ConfirmPaymentRefunded(gomock.Any(), payment.ID).Return(refundedPayment, nil)

	d := refundTestData(q)
	t.Cleanup(func() { _ = d.rdb.Close() })
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPaymentRefund(context.Background(), payment.ID, refund.ID))
}
