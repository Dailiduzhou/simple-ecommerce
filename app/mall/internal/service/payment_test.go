package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePaymentUsecase 是 biz.PaymentUsecase 的测试替身。
// 只实现本次重构关心的方法,其它方法保留 nil,期望在测试中不会被调用。
type fakePaymentUsecase struct {
	create                    func(ctx context.Context, orderID, userID, merchantID int64, payChannel string) (*biz.PaymentDO, error)
	get                       func(ctx context.Context, id int64) (*biz.PaymentDO, error)
	getByOrder                func(ctx context.Context, orderID int64) (*biz.PaymentDO, error)
	prepay                    func(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error)
	prepayForOrder            func(ctx context.Context, args biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error)
	prepayForOrderWithCheckJob func(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error)
	enqueueWechatCheckJobByOutTradeNo func(ctx context.Context, outTradeNo string, checkJob biz.CheckPayArgs) (*biz.MQJob, error)
	enqueueCheckJobByOutTradeNo       func(ctx context.Context, outTradeNo string, channel string, checkJob biz.CheckPayArgs) (*biz.MQJob, error)
	queryOrder                func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error)
	closeOrder                func(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error)
}

func (u *fakePaymentUsecase) CreatePayment(ctx context.Context, orderID, userID, merchantID int64, payChannel string) (*biz.PaymentDO, error) {
	if u.create == nil {
		return nil, errors.New("create not implemented in fake")
	}
	return u.create(ctx, orderID, userID, merchantID, payChannel)
}

func (u *fakePaymentUsecase) GetPayment(ctx context.Context, id int64) (*biz.PaymentDO, error) {
	if u.get == nil {
		return nil, nil
	}
	return u.get(ctx, id)
}

func (u *fakePaymentUsecase) GetPaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	if u.getByOrder == nil {
		return nil, nil
	}
	return u.getByOrder(ctx, orderID)
}

func (u *fakePaymentUsecase) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	if u.prepay == nil {
		return nil, errors.New("prepay not implemented in fake")
	}
	return u.prepay(ctx, req)
}

func (u *fakePaymentUsecase) PrepayForOrder(ctx context.Context, args biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error) {
	if u.prepayForOrder == nil {
		return nil, errors.New("prepayForOrder not implemented in fake")
	}
	return u.prepayForOrder(ctx, args)
}

func (u *fakePaymentUsecase) PrepayForOrderWithCheckJob(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error) {
	if u.prepayForOrderWithCheckJob == nil {
		return nil, nil, errors.New("prepayForOrderWithCheckJob not implemented in fake")
	}
	return u.prepayForOrderWithCheckJob(ctx, args, checkJob, delay)
}

func (u *fakePaymentUsecase) EnqueueWechatCheckJobByOutTradeNo(ctx context.Context, outTradeNo string, checkJob biz.CheckPayArgs) (*biz.MQJob, error) {
	if u.enqueueWechatCheckJobByOutTradeNo == nil {
		return nil, errors.New("enqueueWechatCheckJobByOutTradeNo not implemented in fake")
	}
	return u.enqueueWechatCheckJobByOutTradeNo(ctx, outTradeNo, checkJob)
}

func (u *fakePaymentUsecase) EnqueueCheckJobByOutTradeNo(ctx context.Context, outTradeNo string, channel string, checkJob biz.CheckPayArgs) (*biz.MQJob, error) {
	if u.enqueueCheckJobByOutTradeNo == nil {
		return nil, errors.New("enqueueCheckJobByOutTradeNo not implemented in fake")
	}
	return u.enqueueCheckJobByOutTradeNo(ctx, outTradeNo, channel, checkJob)
}

func (u *fakePaymentUsecase) QueryOrder(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if u.queryOrder == nil {
		return nil, errors.New("queryOrder not implemented in fake")
	}
	return u.queryOrder(ctx, req)
}

func (u *fakePaymentUsecase) CloseOrder(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if u.closeOrder == nil {
		return nil, errors.New("closeOrder not implemented in fake")
	}
	return u.closeOrder(ctx, req)
}

type fakePaymentMQRepo struct {
	enqueue      func(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error)
	enqueueInTx  func(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error)
	get          func(ctx context.Context, jobID int64) (*biz.MQJob, error)
}

func (r *fakePaymentMQRepo) EnqueueCheckPay(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	return r.enqueue(ctx, args, scheduledAt)
}

func (r *fakePaymentMQRepo) EnqueueCheckPayTx(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	if r.enqueueInTx != nil {
		return r.enqueueInTx(ctx, args, scheduledAt)
	}
	return r.enqueue(ctx, args, scheduledAt)
}

func (r *fakePaymentMQRepo) GetMQJob(ctx context.Context, jobID int64) (*biz.MQJob, error) {
	return r.get(ctx, jobID)
}

// —— 统一支付入口 CreatePayment ——

// TestPaymentService_CreatePayment_WechatJSAPI 验证微信 JSAPI 渠道:
//  - 必传 openid,否则 400;
//  - 返回 action_type=WECHAT_INVOKE;
//  - payload 包含完整的 JSAPI 唤起参数。
func TestPaymentService_CreatePayment_WechatJSAPI(t *testing.T) {
	s := newPaymentServiceForUnified(&fakePaymentUsecase{
		prepayForOrderWithCheckJob: func(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error) {
			assert.Equal(t, "merchant-order-1", args.OrderNo)
			assert.Equal(t, string(biz.Wechat), args.Channel)
			assert.Equal(t, "openid-123", args.ExtraParams["openid"])
			assert.Equal(t, "prepay_auto", checkJob.Source)
			assert.Equal(t, "wechat", checkJob.Channel)
			assert.Equal(t, 5*time.Second, delay)
			return &biz.PrepayForOrderResult{
				Payment: &biz.PaymentDO{ID: 1, OutTradeNo: "otn-1"},
				Prepay: &biz.PaymentPrepayResult{
					AppID:     "wx-app",
					TimeStamp: "1700000000",
					NonceStr:  "nonce",
					Package:   "prepay_id=prepay-1",
					SignType:  "MD5",
					PaySign:   "sign-abc",
				},
			}, nil, nil
		},
	})

	reply, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo:     "merchant-order-1",
		Channel:     pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI,
		ClientIp:    "1.2.3.4",
		ExtraParams: map[string]string{"openid": "openid-123"},
	})
	require.NoError(t, err)
	assert.Equal(t, ActionTypeWechatInvoke, reply.ActionType)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(reply.Payload), &payload))
	assert.Equal(t, "wx-app", payload["appId"])
	assert.Equal(t, "prepay_id=prepay-1", payload["package"])
	assert.Equal(t, "MD5", payload["signType"])
	assert.Equal(t, "sign-abc", payload["paySign"])
}

// TestPaymentService_CreatePayment_WechatJSAPIRequiresOpenid 校验 openid 必传。
func TestPaymentService_CreatePayment_WechatJSAPIRequiresOpenid(t *testing.T) {
	s := NewPaymentService(&fakePaymentUsecase{
		prepayForOrder: func(ctx context.Context, args biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error) {
			t.Fatalf("PrepayForOrder should not be called when openid is missing")
			return nil, nil
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "merchant-order-1",
		Channel: pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI,
	})
	require.Error(t, err)
	assert.Nil(t, got)
	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusBadRequest), se.Code)
	assert.Equal(t, "OPENID_REQUIRED", se.Reason)
}

// TestPaymentService_CreatePayment_WechatNative 验证 NATIVE 扫码:走 URL_REDIRECT。
func TestPaymentService_CreatePayment_WechatNative(t *testing.T) {
	s := newPaymentServiceForUnified(&fakePaymentUsecase{
		prepayForOrderWithCheckJob: func(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error) {
			return &biz.PrepayForOrderResult{
				Payment: &biz.PaymentDO{OutTradeNo: "otn-2"},
				Prepay:  &biz.PaymentPrepayResult{CodeURL: "weixin://wxpay/bizpayurl?pr=xxx"},
			}, nil, nil
		},
	})

	reply, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "merchant-order-2",
		Channel: pb.PayChannel_PAY_CHANNEL_WECHAT_NATIVE,
	})
	require.NoError(t, err)
	assert.Equal(t, ActionTypeURLRedirect, reply.ActionType)

	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(reply.Payload), &payload))
	assert.Contains(t, payload["url"], "weixin://wxpay/bizpayurl")
}

// TestPaymentService_CreatePayment_AutoEnqueuesWechatCheckJob 验证微信支付 prepay 成功后
// 会通过 PrepayForOrderWithCheckJob 自动入队一个轮询任务,且默认配置被正确传入。
func TestPaymentService_CreatePayment_AutoEnqueuesWechatCheckJob(t *testing.T) {
	var gotCheckJob biz.CheckPayArgs
	var gotDelay time.Duration
	s := NewPaymentService(&fakePaymentUsecase{
		prepayForOrderWithCheckJob: func(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error) {
			gotCheckJob = checkJob
			gotDelay = delay
			return &biz.PrepayForOrderResult{
				Payment: &biz.PaymentDO{ID: 101, OrderID: 1001, OutTradeNo: "otn-auto"},
				Prepay:  &biz.PaymentPrepayResult{},
			}, &biz.MQJob{ID: 202, Kind: biz.CheckPayJobKind, Queue: "payments"}, nil
		},
	}, nil, nil, log.DefaultLogger)

	_, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "merchant-order-auto",
		Channel: pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI,
		ExtraParams: map[string]string{
			"openid": "openid-auto",
		},
	})
	require.NoError(t, err)

	assert.Equal(t, "wechat", gotCheckJob.Channel)
	assert.Equal(t, "prepay_auto", gotCheckJob.Source)
	assert.Equal(t, 30, gotCheckJob.MaxPolls)
	assert.Equal(t, 10, gotCheckJob.PollIntervalSeconds)
	assert.Equal(t, 5*time.Second, gotDelay)
}

// TestPaymentService_CreatePayment_AlipayWap 验证支付宝 WAP:走 URL_REDIRECT
// (当前 adapter 仅实现 precreate,真实 WAP 应走 FORM_SUBMIT,这里以实现现状为准)。
func TestPaymentService_CreatePayment_AlipayWap(t *testing.T) {
	s := newPaymentServiceForUnified(&fakePaymentUsecase{
		prepayForOrder: func(ctx context.Context, args biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error) {
			assert.Equal(t, string(biz.Alipay), args.Channel)
			return &biz.PrepayForOrderResult{
				Payment: &biz.PaymentDO{OutTradeNo: "otn-3"},
				Prepay:  &biz.PaymentPrepayResult{CodeURL: "https://qr.alipay.com/xxx"},
			}, nil
		},
	})

	reply, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "merchant-order-3",
		Channel: pb.PayChannel_PAY_CHANNEL_ALIPAY_WAP,
	})
	require.NoError(t, err)
	assert.Equal(t, ActionTypeURLRedirect, reply.ActionType)
}

// TestPaymentService_CreatePayment_OrderNotFound 验证 order_no 不存在时返回 404。
func TestPaymentService_CreatePayment_OrderNotFound(t *testing.T) {
	s := NewPaymentService(&fakePaymentUsecase{
		prepayForOrderWithCheckJob: func(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error) {
			return nil, nil, pgx.ErrNoRows
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "missing-order",
		Channel: pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI,
		ExtraParams: map[string]string{"openid": "openid-1"},
	})
	require.Error(t, err)
	assert.Nil(t, got)
	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusNotFound), se.Code)
	assert.Equal(t, "ORDER_NOT_FOUND", se.Reason)
}

// TestPaymentService_CreatePayment_PropagatesAdapterError 验证三方错误原样上抛。
func TestPaymentService_CreatePayment_PropagatesAdapterError(t *testing.T) {
	wantErr := errors.New("gateway timeout")
	s := NewPaymentService(&fakePaymentUsecase{
		prepayForOrderWithCheckJob: func(ctx context.Context, args biz.PrepayForOrderArgs, checkJob biz.CheckPayArgs, delay time.Duration) (*biz.PrepayForOrderResult, *biz.MQJob, error) {
			return nil, nil, wantErr
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "merchant-order-4",
		Channel: pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI,
		ExtraParams: map[string]string{"openid": "openid-1"},
	})
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}

// TestPaymentService_CreatePayment_RejectsUnspecifiedChannel 验证 0 值 channel 被拒。
func TestPaymentService_CreatePayment_RejectsUnspecifiedChannel(t *testing.T) {
	s := NewPaymentService(&fakePaymentUsecase{
		prepayForOrder: func(ctx context.Context, args biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error) {
			t.Fatalf("PrepayForOrder should not be called for unspecified channel")
			return nil, nil
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.CreatePayment(context.Background(), &pb.CreatePaymentReq{
		OrderNo: "merchant-order-5",
		Channel: pb.PayChannel_PAY_CHANNEL_UNSPECIFIED,
	})
	require.Error(t, err)
	assert.Nil(t, got)
	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusBadRequest), se.Code)
	assert.Equal(t, "PAY_CHANNEL_INVALID", se.Reason)
}

// —— 统一查询 QueryPayment ——

func TestPaymentService_QueryPayment_DelegatesToGateway(t *testing.T) {
	s := NewPaymentService(&fakePaymentUsecase{
		queryOrder: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
			assert.Equal(t, "otn-1", req.OutTradeNo)
			assert.Equal(t, string(biz.Wechat), req.Channel)
			return &biz.PaymentQueryResult{
				OutTradeNo:    req.OutTradeNo,
				TransactionID: "wx-tx-1",
				TradeState:    biz.TradeStateSuccess,
				TotalAmount:   9900,
			}, nil
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.QueryPayment(context.Background(), &pb.QueryPaymentReq{
		OutTradeNo: "otn-1",
		Channel:    pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI,
	})
	require.NoError(t, err)
	assert.Equal(t, "otn-1", got.OutTradeNo)
	assert.Equal(t, "wx-tx-1", got.TransactionId)
	assert.Equal(t, pb.TradeState_SUCCESS, got.TradeState)
	assert.Equal(t, int32(9900), got.TotalAmount)
}

func TestPaymentService_QueryPayment_RejectsInvalidChannel(t *testing.T) {
	s := NewPaymentService(&fakePaymentUsecase{
		queryOrder: func(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
			t.Fatalf("QueryOrder should not be called for invalid channel")
			return nil, nil
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.QueryPayment(context.Background(), &pb.QueryPaymentReq{
		OutTradeNo: "otn-1",
		Channel:    pb.PayChannel_PAY_CHANNEL_UNSPECIFIED,
	})
	require.Error(t, err)
	assert.Nil(t, got)
	se := kratoserrors.FromError(err)
	assert.Equal(t, "PAY_CHANNEL_INVALID", se.Reason)
}

// —— 统一关闭 ClosePayment ——

func TestPaymentService_ClosePayment_DelegatesToGateway(t *testing.T) {
	s := NewPaymentService(&fakePaymentUsecase{
		closeOrder: func(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
			assert.Equal(t, "otn-1", req.OutTradeNo)
			assert.Equal(t, string(biz.Alipay), req.Channel)
			return &biz.PaymentCloseResult{Success: true}, nil
		},
	}, nil, nil, log.DefaultLogger)

	got, err := s.ClosePayment(context.Background(), &pb.ClosePaymentReq{
		OutTradeNo: "otn-1",
		Channel:    pb.PayChannel_PAY_CHANNEL_ALIPAY_APP,
	})
	require.NoError(t, err)
	assert.True(t, got.Success)
}

// —— 微信支付异步通知(走 HTTP 路由,不动) ——

func TestPaymentService_HandleWechatPayNotify(t *testing.T) {
	var gotOutTradeNo string
	s := NewPaymentService(&fakePaymentUsecase{
		enqueueWechatCheckJobByOutTradeNo: func(ctx context.Context, outTradeNo string, checkJob biz.CheckPayArgs) (*biz.MQJob, error) {
			gotOutTradeNo = outTradeNo
			assert.Equal(t, "wechat_notify", checkJob.Source)
			assert.Equal(t, "wechat", checkJob.Channel)
			return &biz.MQJob{ID: 303, Kind: biz.CheckPayJobKind, Queue: "payments"}, nil
		},
	}, nil, nil, log.DefaultLogger)
	srv := khttp.NewServer()
	srv.Route("/").POST("/v1/pay/wechat/notify", s.HandleWechatPayNotify)

	// 发送符合微信支付异步通知格式的 XML(无配置 api_key 时跳过验签)。
	body := `<xml>
<return_code><![CDATA[SUCCESS]]></return_code>
<result_code><![CDATA[SUCCESS]]></result_code>
<out_trade_no><![CDATA[order-notify-1]]></out_trade_no>
<transaction_id><![CDATA[wx-tx-1]]></transaction_id>
</xml>`
	req := httptest.NewRequest(http.MethodPost, "/v1/pay/wechat/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "order-notify-1", gotOutTradeNo)
	assert.Equal(t, "application/xml; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "<return_code><![CDATA[SUCCESS]]></return_code>")
}

// —— MQ 任务入队(保持原状) ——

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
	s := NewPaymentService(nil, biz.NewPaymentJobUsecase(repo, log.DefaultLogger), nil, log.DefaultLogger)

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
		Channel:             "wechat",
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
	s := NewPaymentService(nil, biz.NewPaymentJobUsecase(&fakePaymentMQRepo{
		enqueue: func(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
			t.Fatalf("EnqueueCheckPay should not be called")
			return nil, nil
		},
	}, log.DefaultLogger), nil, log.DefaultLogger)

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
	s := NewPaymentService(nil, biz.NewPaymentJobUsecase(repo, log.DefaultLogger), nil, log.DefaultLogger)

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
	s := NewPaymentService(nil, nil, nil, log.DefaultLogger)

	got, err := s.GetMQJob(context.Background(), &pb.GetMQJobRequest{JobId: 101})
	require.Error(t, err)
	assert.Nil(t, got)

	se := kratoserrors.FromError(err)
	assert.Equal(t, int32(http.StatusServiceUnavailable), se.Code)
	assert.Equal(t, "PAYMENT_MQ_NOT_CONFIGURED", se.Reason)
}

// newPaymentServiceForUnified 用 usecase 包装一个 PaymentService,负责保证
// payChannel 之外的所有字段都按 biz 层签名转好。封装减少测试模板代码。
func newPaymentServiceForUnified(u *fakePaymentUsecase) *PaymentService {
	return NewPaymentService(u, nil, nil, log.DefaultLogger)
}
