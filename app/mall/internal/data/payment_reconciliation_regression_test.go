package data

import (
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestResolutionChecksHistoricalProviderRefund(t *testing.T) {
	for _, tc := range []struct {
		name       string
		unresolved bool
		err        error
		want       bool
	}{
		{"no refund anomaly", false, nil, true},
		{"refund hidden by timeout", true, nil, false},
		{"cannot verify", false, errors.New("database unavailable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			payment := statePayment(biz.PaymentStatusSuccess)
			payment.ThirdPartyTxID = pgtype.Text{String: "provider-tx", Valid: true}
			payment.ReconciliationReason = pgtype.Text{String: "job_exhausted", Valid: true}
			order := db.Order{ID: payment.OrderID, UserID: payment.UserID, TotalAmountMinor: payment.AmountMinor, Currency: payment.Currency, Status: biz.OrderStatusPaid, PaidPaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}}
			q.EXPECT().HasUnresolvedProviderRefund(gomock.Any(), payment.ID).Return(tc.unresolved, tc.err)
			allowed, err := canResolvePaymentReconciliation(context.Background(), q, payment, order, nil)
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, tc.want, allowed && err == nil)
		})
	}
}

func TestStaleManualPollCannotEnqueueUnversionedClose(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	payment := statePayment(biz.PaymentStatusPending)
	payment.ReconciliationVersion = 3
	payment.ReconciliationStatus = biz.ReconciliationStatusResolved
	gomock.InOrder(
		q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(payment, nil),
		q.EXPECT().GetOrderForUpdate(gomock.Any(), payment.OrderID).Return(db.Order{ID: payment.OrderID}, nil),
		q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), payment.OrderID).Return([]db.Payment{payment}, nil),
	)
	repo := NewPaymentRepo(nil, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.MarkPayClosePending(context.Background(), biz.CheckPayArgs{PaymentID: payment.ID, ReconciliationVersion: 2}))
}

func TestGetPaymentForJobReadsDatabase(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	payment := statePayment(biz.PaymentStatusSuccess)
	payment.ReconciliationVersion = 3
	payment.ReconciliationStatus = biz.ReconciliationStatusResolved
	q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(payment, nil)
	// No Redis client: this read must never consult the cached payment.
	repo := NewPaymentRepo(&Data{q: q}, nil, log.DefaultLogger)
	got, err := repo.GetPaymentForJob(context.Background(), payment.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, got.ReconciliationVersion)
	require.Equal(t, biz.ReconciliationStatusResolved, got.ReconciliationStatus)
}

func TestManualPollPreservesVersionWhenEnqueuingClose(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	payment := statePayment(biz.PaymentStatusPending)
	payment.ReconciliationVersion = 2
	payment.ReconciliationStatus = biz.ReconciliationStatusProcessing
	closed := payment
	closed.Status = biz.PaymentStatusClosePending
	gomock.InOrder(
		q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(payment, nil),
		q.EXPECT().GetOrderForUpdate(gomock.Any(), payment.OrderID).Return(db.Order{ID: payment.OrderID}, nil),
		q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), payment.OrderID).Return([]db.Payment{payment}, nil),
		q.EXPECT().MarkPaymentClosePending(gomock.Any(), payment.ID).Return(closed, nil),
	)
	jobs := &reconciliationRetryJobs{t: t}
	repo := NewPaymentRepoWithJobs(&Data{q: q}, testTxManager{q: q}, jobs, log.DefaultLogger)
	require.NoError(t, repo.MarkPayClosePending(context.Background(), biz.CheckPayArgs{PaymentID: payment.ID, Provider: "wechat", Trigger: "manual_reconciliation", ReconciliationVersion: 2}))
	require.NotNil(t, jobs.close)
	require.EqualValues(t, 2, jobs.close.ReconciliationVersion)
}
