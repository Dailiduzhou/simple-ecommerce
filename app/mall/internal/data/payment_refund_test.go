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
		Currency: payment.Currency, Purpose: string(biz.RefundOrderCancel), Status: biz.PaymentRefundStatusPending,
	}
	// The initial read only identifies the user. Payment state may change
	// before its row lock is acquired; preparation must use the locked row.
	snapshot := payment
	snapshot.Status = biz.PaymentStatusPending
	gomock.InOrder(
		q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(snapshot, nil),
		q.EXPECT().LockUserForReference(gomock.Any(), payment.UserID).Return(payment.UserID, nil),
		q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), payment.ID).Return(db.Order{ID: payment.OrderID, Status: biz.OrderStatusPaid, PaidPaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}}, nil),
		q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil),
	)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), pgtype.Int8{Int64: payment.ID, Valid: true}).Return(db.OrderRefund{}, pgx.ErrNoRows)
	q.EXPECT().CreateOrderRefund(gomock.Any(), db.CreateOrderRefundParams{
		PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
		OrderID:   payment.OrderID, UserID: payment.UserID, OutRefundNo: "refund_1",
		TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
		Currency: payment.Currency, Purpose: string(biz.RefundOrderCancel),
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
	gomock.InOrder(
		q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(payment, nil),
		q.EXPECT().LockUserForReference(gomock.Any(), payment.UserID).Return(payment.UserID, nil),
		q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), payment.ID).Return(db.Order{ID: payment.OrderID, Status: biz.OrderStatusRefunded, PaidPaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}}, nil),
		q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil),
	)
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
		Currency: payment.Currency, Purpose: string(biz.RefundOrderCancel), Status: biz.PaymentRefundStatusPending,
	}
	refundedPayment := payment
	refundedPayment.Status = biz.PaymentStatusRefunded
	q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), payment.ID).Return(db.Order{ID: payment.OrderID, Status: biz.OrderStatusPaid, PaidPaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}}, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), pgtype.Int8{Int64: payment.ID, Valid: true}).Return(refund, nil)
	q.EXPECT().MarkOrderRefundSuccess(gomock.Any(), db.MarkOrderRefundSuccessParams{
		ID: refund.ID, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
	}).Return(refund, nil)
	q.EXPECT().ConfirmPaymentRefunded(gomock.Any(), payment.ID).Return(refundedPayment, nil)
	// A settled refund must also settle the order: terminal refunded state
	// plus restored stock, in the same transaction.
	refundedOrder := db.Order{ID: payment.OrderID, UserID: payment.UserID, Status: biz.OrderStatusRefunded, IsCompleted: true}
	q.EXPECT().MarkOrderRefunded(gomock.Any(), payment.OrderID).Return(refundedOrder, nil)
	q.EXPECT().RestoreOrderItemStock(gomock.Any(), payment.OrderID).Return(nil)
	q.EXPECT().ListOrderProductCacheTargets(gomock.Any(), payment.OrderID).Return(nil, nil)

	d := refundTestData(q)
	t.Cleanup(func() { _ = d.rdb.Close() })
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPaymentRefund(context.Background(), payment.ID, refund.ID))
}

func TestApplyPaymentRefundRejectsOrderOutsidePaidState(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	payment := statePayment(biz.PaymentStatusSuccess)
	refund := db.OrderRefund{
		ID: 11, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
		OrderID: payment.OrderID, UserID: payment.UserID, OutRefundNo: "refund_1",
		TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
		Currency: payment.Currency, Status: biz.PaymentRefundStatusPending,
	}
	q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), payment.ID).Return(db.Order{ID: payment.OrderID, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), pgtype.Int8{Int64: payment.ID, Valid: true}).Return(refund, nil)
	// An invalid plan is rejected before any settlement writes.

	d := refundTestData(q)
	t.Cleanup(func() { _ = d.rdb.Close() })
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	err := repo.ApplyPaymentRefund(context.Background(), payment.ID, refund.ID)
	require.ErrorIs(t, err, biz.ErrPaymentStateConflict)
}

func TestPreparePaymentRefundStopsOnLookupOrLockFailure(t *testing.T) {
	for _, step := range []string{"lookup", "user", "order", "payment"} {
		for _, failure := range []error{pgx.ErrNoRows, context.DeadlineExceeded} {
			t.Run(step+"/"+failure.Error(), func(t *testing.T) {
				q := mockdb.NewMockQuerier(gomock.NewController(t))
				payment := statePayment(biz.PaymentStatusSuccess)
				steps := []struct {
					name string
					call func(error) *gomock.Call
				}{
					{"lookup", func(err error) *gomock.Call {
						return q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(payment, err)
					}},
					{"user", func(err error) *gomock.Call {
						return q.EXPECT().LockUserForReference(gomock.Any(), payment.UserID).Return(payment.UserID, err)
					}},
					{"order", func(err error) *gomock.Call {
						return q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), payment.ID).Return(db.Order{ID: payment.OrderID}, err)
					}},
					{"payment", func(err error) *gomock.Call {
						return q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, err)
					}},
				}
				var previous *gomock.Call
				for _, s := range steps {
					var err error
					if s.name == step {
						err = failure
					}
					call := s.call(err)
					if previous != nil {
						call.After(previous)
					}
					previous = call
					if err != nil {
						break
					}
				}
				repo := NewPaymentRepo(&Data{q: q}, testTxManager{q: q}, log.DefaultLogger)
				gotPayment, gotRefund, err := repo.PreparePaymentRefund(context.Background(), payment.ID, "refund_failure")
				want := failure
				if failure == pgx.ErrNoRows {
					want = biz.ErrPaymentNotFound
				}
				require.ErrorIs(t, err, want)
				require.Nil(t, gotPayment)
				require.Nil(t, gotRefund)
			})
		}
	}
}

func TestPreparePaymentRefundRejectsChangedReferences(t *testing.T) {
	for _, reference := range []string{"user", "order"} {
		t.Run(reference, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			snapshot := statePayment(biz.PaymentStatusSuccess)
			locked := snapshot
			if reference == "user" {
				locked.UserID++
			} else {
				locked.OrderID++
			}
			gomock.InOrder(
				q.EXPECT().GetPayment(gomock.Any(), snapshot.ID).Return(snapshot, nil),
				q.EXPECT().LockUserForReference(gomock.Any(), snapshot.UserID).Return(snapshot.UserID, nil),
				q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), snapshot.ID).Return(db.Order{ID: snapshot.OrderID}, nil),
				q.EXPECT().GetPaymentForUpdate(gomock.Any(), snapshot.ID).Return(locked, nil),
			)
			repo := NewPaymentRepo(&Data{q: q}, testTxManager{q: q}, log.DefaultLogger)
			payment, refund, err := repo.PreparePaymentRefund(context.Background(), snapshot.ID, "changed_reference")
			require.ErrorIs(t, err, biz.ErrPaymentStateConflict)
			require.Nil(t, payment)
			require.Nil(t, refund)
		})
	}
}
