package job

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeWorkerWechatPayProvider struct {
	query func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error)
}

func (p *fakeWorkerWechatPayProvider) PrepayJSAPI(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error) {
	return nil, errors.New("not implemented")
}

func (p *fakeWorkerWechatPayProvider) QueryOrder(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
	return p.query(ctx, outTradeNo)
}

func (p *fakeWorkerWechatPayProvider) CloseOrder(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error) {
	return nil, errors.New("not implemented")
}

type fakePaymentSyncRepo struct {
	apply   func(ctx context.Context, args biz.CheckWechatPayArgs, result *pb.QueryOrderReply) error
	expired func(ctx context.Context, args biz.CheckWechatPayArgs) error
}

func (r *fakePaymentSyncRepo) ApplyWechatPayQuery(ctx context.Context, args biz.CheckWechatPayArgs, result *pb.QueryOrderReply) error {
	return r.apply(ctx, args, result)
}

func (r *fakePaymentSyncRepo) MarkWechatPayExpired(ctx context.Context, args biz.CheckWechatPayArgs) error {
	return r.expired(ctx, args)
}

func TestCheckWechatPayWorker_ApplyTerminalState(t *testing.T) {
	args := biz.CheckWechatPayArgs{PaymentID: 12, OutTradeNo: "order-12", MaxPolls: 5, PollIntervalSeconds: 30}
	worker := NewCheckWechatPayWorker(
		&fakeWorkerWechatPayProvider{
			query: func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
				assert.Equal(t, "order-12", outTradeNo)
				return &pb.QueryOrderReply{
					OutTradeNo:    outTradeNo,
					TransactionId: "wx-12",
					TradeState:    pb.TradeState_SUCCESS,
					TotalAmount:   9900,
				}, nil
			},
		},
		&fakePaymentSyncRepo{
			apply: func(ctx context.Context, gotArgs biz.CheckWechatPayArgs, result *pb.QueryOrderReply) error {
				assert.Equal(t, args, gotArgs)
				assert.Equal(t, pb.TradeState_SUCCESS, result.TradeState)
				assert.Equal(t, "wx-12", result.TransactionId)
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
			query: func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
				return &pb.QueryOrderReply{OutTradeNo: outTradeNo, TradeState: pb.TradeState_NOTPAY}, nil
			},
		},
		&fakePaymentSyncRepo{
			apply: func(ctx context.Context, args biz.CheckWechatPayArgs, result *pb.QueryOrderReply) error {
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
			query: func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
				return &pb.QueryOrderReply{OutTradeNo: outTradeNo, TradeState: pb.TradeState_USERPAYING}, nil
			},
		},
		&fakePaymentSyncRepo{
			apply: func(ctx context.Context, args biz.CheckWechatPayArgs, result *pb.QueryOrderReply) error {
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
			query: func(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
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
