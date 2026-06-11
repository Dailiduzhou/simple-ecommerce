package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePaymentGateway struct {
	channel string
	prepay  func(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error)
	query   func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error)
	close   func(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error)
}

func (g *fakePaymentGateway) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	return g.prepay(ctx, req)
}

func (g *fakePaymentGateway) QueryOrder(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	return g.query(ctx, req)
}

func (g *fakePaymentGateway) CloseOrder(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	return g.close(ctx, req)
}

type fakePaymentRepo struct {
	create         func(ctx context.Context, args biz.CreatePaymentArgs) (*biz.PaymentDO, error)
	get            func(ctx context.Context, id int64) (*biz.PaymentDO, error)
	getByOrder     func(ctx context.Context, orderID int64) (*biz.PaymentDO, error)
}

func (r *fakePaymentRepo) CreatePayment(ctx context.Context, args biz.CreatePaymentArgs) (*biz.PaymentDO, error) {
	return r.create(ctx, args)
}

func (r *fakePaymentRepo) GetPayment(ctx context.Context, id int64) (*biz.PaymentDO, error) {
	return r.get(ctx, id)
}

func (r *fakePaymentRepo) GetPaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	return r.getByOrder(ctx, orderID)
}

type fakeOrderRepo struct {
	biz.OrderRepo
	getOrder func(ctx context.Context, id int64) (biz.Order, error)
}

func (r *fakeOrderRepo) GetOrder(ctx context.Context, id int64) (biz.Order, error) {
	return r.getOrder(ctx, id)
}

type fakePaymentMQRepo struct {
	enqueue func(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error)
	get     func(ctx context.Context, jobID int64) (*biz.MQJob, error)
}

func (r *fakePaymentMQRepo) EnqueueCheckPay(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	return r.enqueue(ctx, args, scheduledAt)
}

func (r *fakePaymentMQRepo) GetMQJob(ctx context.Context, jobID int64) (*biz.MQJob, error) {
	return r.get(ctx, jobID)
}

func TestPaymentService_PrepayDelegatesToGateway(t *testing.T) {
	s := NewPaymentService(&fakePaymentGateway{
		prepay: func(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
			assert.Equal(t, "order-1", req.OutTradeNo)
			assert.Equal(t, int32(9900), req.TotalAmount)
			assert.Equal(t, string(biz.Wechat), req.Channel)
			return &biz.PaymentPrepayResult{AppID: "appid", Package: "prepay_id=wx123"}, nil
		},
		query: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
			assert.Equal(t, "order-1", req.OutTradeNo)
			assert.Equal(t, string(biz.Wechat), req.Channel)
			return &biz.PaymentQueryResult{OutTradeNo: req.OutTradeNo, TradeState: biz.TradeStateSuccess, TotalAmount: 9900}, nil
		},
		close: func(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
			assert.Equal(t, "order-1", req.OutTradeNo)
			assert.Equal(t, string(biz.Wechat), req.Channel)
			return &biz.PaymentCloseResult{Success: true}, nil
		},
	}, nil, nil, nil)

	prepay, err := s.PrepayJSAPI(context.Background(), &pb.PrepayJSAPIRequest{
		OutTradeNo:  "order-1",
		Description: "test order",
		TotalAmount: 9900,
		Openid:      "openid-1",
		PayChannel:  string(biz.Wechat),
	})
	require.NoError(t, err)
	assert.Equal(t, "appid", prepay.AppId)
	assert.Equal(t, "prepay_id=wx123", prepay.PrepayPackage)

	order, err := s.QueryOrder(context.Background(), &pb.QueryOrderRequest{
		OutTradeNo: "order-1",
		PayChannel: string(biz.Wechat),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.TradeState_SUCCESS, order.TradeState)
	assert.Equal(t, int32(9900), order.TotalAmount)

	closed, err := s.CloseOrder(context.Background(), &pb.CloseOrderRequest{
		OutTradeNo: "order-1",
		PayChannel: string(biz.Wechat),
	})
	require.NoError(t, err)
	assert.True(t, closed.Success)
}

func TestPaymentService_PrepayDefaultsChannelToWechat(t *testing.T) {
	s := NewPaymentService(&fakePaymentGateway{
		prepay: func(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
			assert.Equal(t, string(biz.Wechat), req.Channel, "should default to wechat")
			return &biz.PaymentPrepayResult{}, nil
		},
	}, nil, nil, nil)

	_, err := s.PrepayJSAPI(context.Background(), &pb.PrepayJSAPIRequest{
		OutTradeNo: "order-default",
	})
	require.NoError(t, err)
}

func TestPaymentService_PrepayUsesAlipayChannel(t *testing.T) {
	s := NewPaymentService(&fakePaymentGateway{
		prepay: func(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
			assert.Equal(t, string(biz.Alipay), req.Channel)
			return &biz.PaymentPrepayResult{}, nil
		},
	}, nil, nil, nil)

	_, err := s.PrepayJSAPI(context.Background(), &pb.PrepayJSAPIRequest{
		OutTradeNo: "order-alipay",
		PayChannel: string(biz.Alipay),
	})
	require.NoError(t, err)
}

func TestPaymentService_PrepayPropagatesGatewayError(t *testing.T) {
	wantErr := errors.New("gateway failed")
	s := NewPaymentService(&fakePaymentGateway{
		prepay: func(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
			return nil, wantErr
		},
	}, nil, nil, nil)

	got, err := s.PrepayJSAPI(context.Background(), &pb.PrepayJSAPIRequest{OutTradeNo: "order-2"})
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}

func TestPaymentService_GatewayMissing(t *testing.T) {
	s := NewPaymentService(nil, nil, nil, nil)

	got, err := s.QueryOrder(context.Background(), &pb.QueryOrderRequest{OutTradeNo: "order-3"})
	require.Error(t, err)
	assert.Nil(t, got)

	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusServiceUnavailable), se.Code)
	assert.Equal(t, "PAYMENT_GATEWAY_NOT_CONFIGURED", se.Reason)
}

func TestPaymentService_HandleWechatPayNotify(t *testing.T) {
	s := NewPaymentService(nil, nil, nil, nil)
	srv := khttp.NewServer()
	srv.Route("/").POST("/v1/pay/wechat/notify", s.HandleWechatPayNotify)

	req := httptest.NewRequest(http.MethodPost, "/v1/pay/wechat/notify", strings.NewReader(`{"id":"notify-1"}`))
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"code":"SUCCESS","message":"success"}`, rec.Body.String())
}

func TestPaymentService_CreateWechatPayCheckJob(t *testing.T) {
	var gotArgs biz.CheckPayArgs
	var gotScheduledAt time.Time
	repo := &fakePaymentMQRepo{
		enqueue: func(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
			gotArgs = args
			gotScheduledAt = scheduledAt
			return &biz.MQJob{
				ID:          101,
				Kind:        biz.CheckPayJobKind,
				Queue:       "payments",
				State:       "scheduled",
				Attempt:     0,
				MaxAttempts: args.MaxPolls,
				ArgsJSON:    `{"payment_id":12}`,
				Tags:        []string{"pay-channel-wechat"},
				CreatedAt:   time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
				ScheduledAt: scheduledAt,
			}, nil
		},
	}
	s := NewPaymentService(nil, nil, nil, biz.NewPaymentJobUsecase(repo, log.DefaultLogger))

	before := time.Now().Add(2 * time.Second)
	got, err := s.CreateWechatPayCheckJob(context.Background(), &pb.CreateWechatPayCheckJobRequest{
		PaymentId:           12,
		OrderId:             34,
		OutTradeNo:          "order-12",
		DelaySeconds:        3,
		MaxPolls:            8,
		PollIntervalSeconds: 45,
		Source:              "prepay",
	})
	after := time.Now().Add(4 * time.Second)

	require.NoError(t, err)
	assert.Equal(t, biz.CheckPayArgs{
		PaymentID:           12,
		OrderID:             34,
		OutTradeNo:          "order-12",
		MaxPolls:            8,
		PollIntervalSeconds: 45,
		Source:              "prepay",
	}, gotArgs)
	assert.False(t, gotScheduledAt.IsZero())
	assert.True(t, !gotScheduledAt.Before(before) && !gotScheduledAt.After(after), gotScheduledAt)
	assert.Equal(t, int64(101), got.JobId)
	assert.Equal(t, biz.CheckPayJobKind, got.Kind)
	assert.Equal(t, "payments", got.Queue)
	assert.Equal(t, int32(8), got.MaxAttempts)
	assert.Equal(t, `{"payment_id":12}`, got.ArgsJson)
	require.NotNil(t, got.CreatedAt)
	require.NotNil(t, got.ScheduledAt)
}

func TestPaymentService_CreateWechatPayCheckJobRejectsNegativeDelay(t *testing.T) {
	s := NewPaymentService(nil, nil, nil, biz.NewPaymentJobUsecase(&fakePaymentMQRepo{
		enqueue: func(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
			t.Fatalf("EnqueueCheckPay should not be called")
			return nil, nil
		},
	}, log.DefaultLogger))

	got, err := s.CreateWechatPayCheckJob(context.Background(), &pb.CreateWechatPayCheckJobRequest{
		PaymentId:    12,
		OutTradeNo:   "order-12",
		DelaySeconds: -1,
	})
	require.Error(t, err)
	assert.Nil(t, got)

	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusBadRequest), se.Code)
	assert.Equal(t, "DELAY_SECONDS_INVALID", se.Reason)
}

func TestPaymentService_GetMQJob(t *testing.T) {
	attemptedAt := time.Date(2026, 6, 1, 10, 1, 0, 0, time.UTC)
	errorAt := time.Date(2026, 6, 1, 10, 1, 1, 0, time.UTC)
	repo := &fakePaymentMQRepo{
		get: func(ctx context.Context, jobID int64) (*biz.MQJob, error) {
			assert.Equal(t, int64(101), jobID)
			return &biz.MQJob{
				ID:          jobID,
				Kind:        biz.CheckPayJobKind,
				Queue:       "payments",
				State:       "retryable",
				Attempt:     1,
				MaxAttempts: 5,
				ArgsJSON:    `{"payment_id":12}`,
				Tags:        []string{"pay-channel-wechat", "payment-12"},
				AttemptedAt: &attemptedAt,
				Errors: []biz.MQJobError{{
					Attempt: 1,
					Error:   "not paid",
					At:      errorAt,
				}},
			}, nil
		},
	}
	s := NewPaymentService(nil, nil, nil, biz.NewPaymentJobUsecase(repo, log.DefaultLogger))

	got, err := s.GetMQJob(context.Background(), &pb.GetMQJobRequest{JobId: 101})
	require.NoError(t, err)
	assert.Equal(t, int64(101), got.JobId)
	assert.Equal(t, "retryable", got.State)
	assert.Equal(t, int32(1), got.Attempt)
	assert.Equal(t, []string{"pay-channel-wechat", "payment-12"}, got.Tags)
	require.NotNil(t, got.AttemptedAt)
	require.Len(t, got.Errors, 1)
	assert.Equal(t, int32(1), got.Errors[0].Attempt)
	assert.Equal(t, "not paid", got.Errors[0].Error)
	require.NotNil(t, got.Errors[0].At)
}

func TestPaymentService_PaymentMQMissing(t *testing.T) {
	s := NewPaymentService(nil, nil, nil, nil)

	got, err := s.GetMQJob(context.Background(), &pb.GetMQJobRequest{JobId: 101})
	require.Error(t, err)
	assert.Nil(t, got)

	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusServiceUnavailable), se.Code)
	assert.Equal(t, "PAYMENT_MQ_NOT_CONFIGURED", se.Reason)
}
