package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWorkerWechatPayProvider struct {
	query func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error)
}

func (p *fakeWorkerWechatPayProvider) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	return nil, errors.New("not implemented")
}

func (p *fakeWorkerWechatPayProvider) QueryOrder(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	return p.query(ctx, req)
}

func (p *fakeWorkerWechatPayProvider) CloseOrder(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	return nil, errors.New("not implemented")
}

func (p *fakeWorkerWechatPayProvider) Channel() string {
	return biz.PayChannelWechat
}

type fakePaymentSyncRepo struct {
	apply   func(ctx context.Context, args biz.CheckWechatPayArgs, result *biz.PaymentQueryResult) error
	expired func(ctx context.Context, args biz.CheckWechatPayArgs) error
}

func (r *fakePaymentSyncRepo) ApplyWechatPayQuery(ctx context.Context, args biz.CheckWechatPayArgs, result *biz.PaymentQueryResult) error {
	return r.apply(ctx, args, result)
}

func (r *fakePaymentSyncRepo) MarkWechatPayExpired(ctx context.Context, args biz.CheckWechatPayArgs) error {
	return r.expired(ctx, args)
}

func TestCheckWechatPayWorker_ApplyTerminalState(t *testing.T) {
	args := biz.CheckWechatPayArgs{PaymentID: 12, OutTradeNo: "order-12", MaxPolls: 5, PollIntervalSeconds: 30}
	worker := NewCheckWechatPayWorker(
		&fakeWorkerWechatPayProvider{
			query: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
				assert.Equal(t, "order-12", req.OutTradeNo)
				return &biz.PaymentQueryResult{
					OutTradeNo:    req.OutTradeNo,
					TransactionID: "wx-12",
					TradeState:    biz.TradeStateSuccess,
					TotalAmount:   9900,
				}, nil
			},
		},
		&fakePaymentSyncRepo{
			apply: func(ctx context.Context, gotArgs biz.CheckWechatPayArgs, result *biz.PaymentQueryResult) error {
				assert.Equal(t, args, gotArgs)
				assert.Equal(t, biz.TradeStateSuccess, result.TradeState)
				assert.Equal(t, "wx-12", result.TransactionID)
				return nil
			},
			expired: func(ctx context.Context, args biz.CheckWechatPayArgs) error {
				t.Fatalf("MarkWechatPayExpired should not be called")
				return nil
			},
		},
		log.DefaultLogger,
	)

	err := worker.Work(context.Background(), &river.Job[biz.CheckWechatPayArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   args,
	})
	require.NoError(t, err)
}

func TestCheckWechatPayWorker_RetriesPendingState(t *testing.T) {
	worker := NewCheckWechatPayWorker(
		&fakeWorkerWechatPayProvider{
			query: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
				return &biz.PaymentQueryResult{OutTradeNo: req.OutTradeNo, TradeState: biz.TradeStateNotPay}, nil
			},
		},
		&fakePaymentSyncRepo{
			apply: func(ctx context.Context, args biz.CheckWechatPayArgs, result *biz.PaymentQueryResult) error {
				t.Fatalf("ApplyWechatPayQuery should not be called")
				return nil
			},
			expired: func(ctx context.Context, args biz.CheckWechatPayArgs) error {
				t.Fatalf("MarkWechatPayExpired should not be called")
				return nil
			},
		},
		log.DefaultLogger,
	)

	err := worker.Work(context.Background(), &river.Job[biz.CheckWechatPayArgs]{
		JobRow: &rivertype.JobRow{Attempt: 2},
		Args:   biz.CheckWechatPayArgs{PaymentID: 12, OutTradeNo: "order-12", MaxPolls: 5},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still pending")
}

func TestCheckWechatPayWorker_MarksExpiredOnLastPendingAttempt(t *testing.T) {
	args := biz.CheckWechatPayArgs{PaymentID: 12, OrderID: 34, OutTradeNo: "order-12", MaxPolls: 3, PollIntervalSeconds: 30}
	worker := NewCheckWechatPayWorker(
		&fakeWorkerWechatPayProvider{
			query: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
				return &biz.PaymentQueryResult{OutTradeNo: req.OutTradeNo, TradeState: biz.TradeStateUserPaying}, nil
			},
		},
		&fakePaymentSyncRepo{
			apply: func(ctx context.Context, args biz.CheckWechatPayArgs, result *biz.PaymentQueryResult) error {
				t.Fatalf("ApplyWechatPayQuery should not be called")
				return nil
			},
			expired: func(ctx context.Context, gotArgs biz.CheckWechatPayArgs) error {
				assert.Equal(t, args, gotArgs)
				return nil
			},
		},
		log.DefaultLogger,
	)

	err := worker.Work(context.Background(), &river.Job[biz.CheckWechatPayArgs]{
		JobRow: &rivertype.JobRow{Attempt: 3},
		Args:   args,
	})
	require.NoError(t, err)
}

func TestCheckWechatPayWorker_CancelsInvalidArgs(t *testing.T) {
	worker := NewCheckWechatPayWorker(
		&fakeWorkerWechatPayProvider{
			query: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
				t.Fatalf("QueryOrder should not be called")
				return nil, nil
			},
		},
		&fakePaymentSyncRepo{},
		log.DefaultLogger,
	)

	err := worker.Work(context.Background(), &river.Job[biz.CheckWechatPayArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   biz.CheckWechatPayArgs{OutTradeNo: "order-12"},
	})
	require.Error(t, err)

	var cancelErr *river.JobCancelError
	assert.True(t, errors.As(err, &cancelErr), err)
	assert.Contains(t, err.Error(), "payment_id is required")
}

func TestCheckWechatPayWorker_NextRetryUsesPollInterval(t *testing.T) {
	worker := NewCheckWechatPayWorker(nil, nil, log.DefaultLogger)

	before := time.Now().Add(9 * time.Second)
	got := worker.NextRetry(&river.Job[biz.CheckWechatPayArgs]{
		Args: biz.CheckWechatPayArgs{PollIntervalSeconds: 10},
	})
	after := time.Now().Add(11 * time.Second)

	assert.True(t, !got.Before(before) && !got.After(after), got)
}
