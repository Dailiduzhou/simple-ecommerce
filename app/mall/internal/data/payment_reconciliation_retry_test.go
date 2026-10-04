package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type reconciliationRetryJobs struct {
	biz.PaymentMQRepo
	t     *testing.T
	check *biz.CheckPayArgs
	close *biz.ClosePayArgs
}

func (j *reconciliationRetryJobs) EnqueueCheckPayTx(ctx context.Context, args biz.CheckPayArgs, at time.Time) (*biz.MQJob, error) {
	require.True(j.t, inTransaction(ctx))
	require.True(j.t, at.IsZero())
	j.check = &args
	return &biz.MQJob{ID: 123}, nil
}
func (j *reconciliationRetryJobs) EnqueueClosePayTx(ctx context.Context, args biz.ClosePayArgs, at time.Time) (*biz.MQJob, error) {
	require.True(j.t, inTransaction(ctx))
	require.True(j.t, at.IsZero())
	j.close = &args
	return &biz.MQJob{ID: 123}, nil
}

func TestReconciliationRetryResumesClosePendingOperation(t *testing.T) {
	for _, status := range []string{biz.PaymentStatusPending, biz.PaymentStatusClosePending} {
		t.Run(status, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			payment := statePayment(status)
			payment.PayChannel = "alipay:app"
			payment.ReconciliationVersion = 1
			payment.ReconciliationStatus = biz.ReconciliationStatusRequired
			q.EXPECT().GetPayment(gomock.Any(), payment.ID).Return(payment, nil)
			q.EXPECT().GetOrderForUpdate(gomock.Any(), payment.OrderID).Return(db.Order{ID: payment.OrderID}, nil)
			q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), payment.OrderID).Return([]db.Payment{payment}, nil)
			q.EXPECT().GetReconciliationAction(gomock.Any(), gomock.Any()).Return(db.PaymentReconciliationAction{}, pgx.ErrNoRows)
			changed := payment
			changed.ReconciliationVersion = 2
			changed.ReconciliationStatus = biz.ReconciliationStatusProcessing
			q.EXPECT().RetryPaymentReconciliation(gomock.Any(), db.RetryPaymentReconciliationParams{ID: payment.ID, ReconciliationVersion: 1}).Return(changed, nil)
			q.EXPECT().CreateReconciliationAction(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreateReconciliationActionParams) (db.PaymentReconciliationAction, error) {
				require.EqualValues(t, 123, args.RiverJobID.Int64)
				require.True(t, args.RiverJobID.Valid)
				require.Equal(t, biz.ReconciliationStatusProcessing, args.ToStatus)
				return db.PaymentReconciliationAction{ID: 9, RiverJobID: args.RiverJobID}, nil
			})
			jobs := &reconciliationRetryJobs{t: t}
			repo := NewPaymentReconciliationRepo(&Data{q: q}, testTxManager{q: q}, jobs, log.DefaultLogger)
			action, err := repo.ApplyReconciliation(context.Background(), biz.Actor{ID: 99, Admin: true}, biz.ReconciliationInput{
				PaymentID: payment.ID, ExpectedVersion: 1, Action: biz.ReconciliationActionRetry,
				IdempotencyKey: "retry-operation", Reason: "provider recovered",
			})
			require.NoError(t, err)
			require.EqualValues(t, 123, action.JobID)
			if status == biz.PaymentStatusClosePending {
				require.Nil(t, jobs.check)
				require.Equal(t, &biz.ClosePayArgs{PaymentID: payment.ID, Provider: "alipay", Reason: "manual_reconciliation"}, jobs.close)
			} else {
				require.Nil(t, jobs.close)
				require.NotNil(t, jobs.check)
				require.Equal(t, "manual_reconciliation", jobs.check.Trigger)
			}
		})
	}
}
