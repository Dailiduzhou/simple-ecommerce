package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 前端动作类型常量。返回给前端的 CreatePaymentReply.action_type 取这些值,
// 前端 switch 决定如何消费 payload。文档见 api/payment/v1/payment.proto。
const (
	// ActionTypeFormSubmit: 前端把 payload 渲染成 <form> 自动提交(支付宝 WAP)。
	ActionTypeFormSubmit = "FORM_SUBMIT"
	// ActionTypeURLRedirect: 前端直接 location.href = payload.code_url(扫码 / 跳转链接)。
	ActionTypeURLRedirect = "URL_REDIRECT"
	// ActionTypeWechatInvoke: 前端用 WeixinJSBridge / jweixin 唤起支付(JSAPI / APP)。
	ActionTypeWechatInvoke = "WECHAT_INVOKE"
)

// PaymentService 负责把 proto 的统一支付入口翻译成 biz 层调用,并把
// 三方返回的 PaymentPrepayResult 编码成前端能直接消费的动作指令。
type PaymentService struct {
	pb.UnimplementedPaymentServer
	paymentUc   biz.PaymentUsecase
	paymentJobs biz.PaymentJobUsecase
}

func NewPaymentService(paymentUc biz.PaymentUsecase, paymentJobs biz.PaymentJobUsecase) *PaymentService {
	return &PaymentService{
		paymentUc:   paymentUc,
		paymentJobs: paymentJobs,
	}
}

// CreatePayment 是统一支付入口。流程:
//  1. proto.PayChannel 枚举 -> biz channel 字符串(wechat / alipay);
//  2. 调 usecase.PrepayForOrder 完成 order_no -> payment -> prepay;
//  3. 根据原始 proto 枚举编码 action_type + payload JSON。
func (s *PaymentService) CreatePayment(ctx context.Context, req *pb.CreatePaymentReq) (*pb.CreatePaymentReply, error) {
	if req == nil {
		return nil, errors.BadRequest("PAYMENT_REQ_REQUIRED", "request is required")
	}
	if req.OrderNo == "" {
		return nil, errors.BadRequest("ORDER_NO_REQUIRED", "order_no is required")
	}
	bizChannel, err := protoToBizChannel(req.Channel)
	if err != nil {
		return nil, err
	}
	if req.Channel == pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI {
		// JSAPI 渠道必须带 openid(微信侧强校验)。
		if req.ExtraParams == nil || req.ExtraParams["openid"] == "" {
			return nil, errors.BadRequest("OPENID_REQUIRED", "openid is required for WECHAT_JSAPI (set extra_params[\"openid\"])")
		}
	}

	result, err := s.paymentUc.PrepayForOrder(ctx, biz.PrepayForOrderArgs{
		OrderNo:     req.OrderNo,
		Channel:     bizChannel,
		ClientIP:    req.ClientIp,
		ExtraParams: req.ExtraParams,
		Description: req.Description,
		TotalAmount: req.TotalAmount,
	})
	if err != nil {
		// 订单不存在时 pgx.ErrNoRows 上抛,转换成 404。
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.NotFound("ORDER_NOT_FOUND", "order not found by order_no")
		}
		return nil, err
	}

	actionType, payload, err := encodePrepayPayload(req.Channel, result.Prepay)
	if err != nil {
		return nil, err
	}
	return &pb.CreatePaymentReply{
		ActionType: actionType,
		Payload:    payload,
	}, nil
}

// QueryPayment 统一查询入口。
func (s *PaymentService) QueryPayment(ctx context.Context, req *pb.QueryPaymentReq) (*pb.QueryPaymentReply, error) {
	if req == nil {
		return nil, errors.BadRequest("PAYMENT_REQ_REQUIRED", "request is required")
	}
	bizChannel, err := protoToBizChannel(req.Channel)
	if err != nil {
		return nil, err
	}
	result, err := s.paymentUc.QueryOrder(ctx, biz.PaymentQueryRequest{
		Channel:    bizChannel,
		OutTradeNo: req.OutTradeNo,
	})
	if err != nil {
		return nil, err
	}
	return &pb.QueryPaymentReply{
		OutTradeNo:    result.OutTradeNo,
		TransactionId: result.TransactionID,
		TradeState:    toProtoTradeState(result.TradeState),
		TotalAmount:   result.TotalAmount,
	}, nil
}

// ClosePayment 统一关闭入口。
func (s *PaymentService) ClosePayment(ctx context.Context, req *pb.ClosePaymentReq) (*pb.ClosePaymentReply, error) {
	if req == nil {
		return nil, errors.BadRequest("PAYMENT_REQ_REQUIRED", "request is required")
	}
	bizChannel, err := protoToBizChannel(req.Channel)
	if err != nil {
		return nil, err
	}
	result, err := s.paymentUc.CloseOrder(ctx, biz.PaymentCloseRequest{
		Channel:    bizChannel,
		OutTradeNo: req.OutTradeNo,
	})
	if err != nil {
		return nil, err
	}
	return &pb.ClosePaymentReply{Success: result.Success}, nil
}

// —— 以下 RPC 不在本次渠道统一重构范围,保留旧实现 ——

// GetPayment 按 int64 id 查询支付流水。
func (s *PaymentService) GetPayment(ctx context.Context, req *pb.GetPaymentRequest) (*pb.PaymentInfo, error) {
	payment, err := s.paymentUc.GetPayment(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

// GetPaymentByOrder 按订单 int64 id 查询支付流水。
func (s *PaymentService) GetPaymentByOrder(ctx context.Context, req *pb.GetPaymentByOrderRequest) (*pb.PaymentInfo, error) {
	payment, err := s.paymentUc.GetPaymentByOrder(ctx, req.OrderId)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

// RefundPayment 退款入口(保持原状,本次不动)。
func (s *PaymentService) RefundPayment(ctx context.Context, req *pb.RefundPaymentRequest) (*pb.RefundPaymentReply, error) {
	return &pb.RefundPaymentReply{}, nil
}

// CreateWechatPayCheckJob 入队一个轮询支付状态的 MQ 任务。
func (s *PaymentService) CreateWechatPayCheckJob(ctx context.Context, req *pb.CreateWechatPayCheckJobRequest) (*pb.MQJobInfo, error) {
	if s.paymentJobs == nil {
		return nil, paymentMQMissing()
	}
	if req.DelaySeconds < 0 {
		return nil, errors.BadRequest("DELAY_SECONDS_INVALID", "delay_seconds must be greater than or equal to 0")
	}
	job, err := s.paymentJobs.EnqueueCheckPay(ctx, biz.CheckPayArgs{
		PaymentID:           req.PaymentId,
		OrderID:             req.OrderId,
		OutTradeNo:          req.OutTradeNo,
		MaxPolls:            int(req.MaxPolls),
		PollIntervalSeconds: int(req.PollIntervalSeconds),
		Source:              req.Source,
	}, time.Duration(req.DelaySeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	return toProtoMQJob(job), nil
}

// GetMQJob 查询 MQ 任务状态。
func (s *PaymentService) GetMQJob(ctx context.Context, req *pb.GetMQJobRequest) (*pb.MQJobInfo, error) {
	if s.paymentJobs == nil {
		return nil, paymentMQMissing()
	}
	job, err := s.paymentJobs.GetMQJob(ctx, req.JobId)
	if err != nil {
		return nil, err
	}
	return toProtoMQJob(job), nil
}

// HandleWechatPayNotify 处理微信支付异步通知的 HTTP 回调(不走 gRPC)。
func (s *PaymentService) HandleWechatPayNotify(ctx khttp.Context) error {
	if _, err := io.ReadAll(ctx.Request().Body); err != nil {
		return errors.BadRequest("WECHAT_PAY_NOTIFY_BODY", err.Error())
	}
	return ctx.JSON(200, wechatPayNotifyAck{
		Code:    "SUCCESS",
		Message: "success",
	})
}

type wechatPayNotifyAck struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// protoToBizChannel 把 proto.PayChannel 枚举映射成 biz 层 channel 字符串。
// proto 枚举保留渠道细分(JSAPI/NATIVE/WAP/APP),biz 层只关心适配器分桶。
// 未知 / 0 值(unspecified)拒绝。
func protoToBizChannel(c pb.PayChannel) (string, error) {
	switch c {
	case pb.PayChannel_PAY_CHANNEL_ALIPAY_WAP, pb.PayChannel_PAY_CHANNEL_ALIPAY_APP:
		return string(biz.Alipay), nil
	case pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI, pb.PayChannel_PAY_CHANNEL_WECHAT_NATIVE, pb.PayChannel_PAY_CHANNEL_WECHAT_APP:
		return string(biz.Wechat), nil
	default:
		return "", errors.BadRequest("PAY_CHANNEL_INVALID", fmt.Sprintf("pay channel %d is not supported", int32(c)))
	}
}

// encodePrepayPayload 根据 proto 渠道子类型把三方返回的 PaymentPrepayResult
// 编码成前端可用的动作指令 + JSON 字符串。
//
// 渠道映射:
//   - ALIPAY_WAP   -> FORM_SUBMIT, payload = { action_url, method, form_data }
//   - ALIPAY_APP   -> URL_REDIRECT, payload = { url }
//   - WECHAT_JSAPI -> WECHAT_INVOKE, payload = { appId, timeStamp, nonceStr, package, signType, paySign }
//   - WECHAT_NATIVE-> URL_REDIRECT, payload = { url: code_url }
//   - WECHAT_APP   -> WECHAT_INVOKE, payload = 同 JSAPI
func encodePrepayPayload(channel pb.PayChannel, prepay *biz.PaymentPrepayResult) (string, string, error) {
	if prepay == nil {
		return "", "", errors.InternalServer("PREPAY_RESULT_EMPTY", "prepay result is empty")
	}
	switch channel {
	case pb.PayChannel_PAY_CHANNEL_ALIPAY_WAP:
		return ActionTypeURLRedirect, mustJSON(map[string]string{"url": prepay.CodeURL}), nil
	case pb.PayChannel_PAY_CHANNEL_ALIPAY_APP:
		return ActionTypeURLRedirect, mustJSON(map[string]string{"url": prepay.CodeURL}), nil
	case pb.PayChannel_PAY_CHANNEL_WECHAT_JSAPI:
		return ActionTypeWechatInvoke, mustJSON(map[string]string{
			"appId":     prepay.AppID,
			"timeStamp": prepay.TimeStamp,
			"nonceStr":  prepay.NonceStr,
			"package":   prepay.Package,
			"signType":  prepay.SignType,
			"paySign":   prepay.PaySign,
		}), nil
	case pb.PayChannel_PAY_CHANNEL_WECHAT_NATIVE:
		return ActionTypeURLRedirect, mustJSON(map[string]string{"url": prepay.CodeURL}), nil
	case pb.PayChannel_PAY_CHANNEL_WECHAT_APP:
		return ActionTypeWechatInvoke, mustJSON(map[string]string{
			"appId":     prepay.AppID,
			"timeStamp": prepay.TimeStamp,
			"nonceStr":  prepay.NonceStr,
			"package":   prepay.Package,
			"signType":  prepay.SignType,
			"paySign":   prepay.PaySign,
		}), nil
	default:
		return "", "", errors.BadRequest("PAY_CHANNEL_INVALID", fmt.Sprintf("pay channel %d is not supported", int32(channel)))
	}
}

// mustJSON 把任意结构序列化为 JSON 字符串,序列化失败返回 "{}"。
// 当前调用点都是 map[string]string,失败概率为 0。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func paymentMQMissing() error {
	return errors.ServiceUnavailable("PAYMENT_MQ_NOT_CONFIGURED", "payment mq is not configured")
}

func toProtoPaymentInfo(p *biz.PaymentDO) *pb.PaymentInfo {
	if p == nil {
		return nil
	}
	result := &pb.PaymentInfo{
		Id:             p.ID,
		OrderId:        p.OrderID,
		UserId:         p.UserID,
		MerchantId:     p.MerchantID,
		Amount:         formatAmount(p.Amount),
		Status:         p.Status,
		PayChannel:     p.PayChannel,
		ThirdPartyTxId: p.ThirdPartyTxID,
		OutTradeNo:     p.OutTradeNo,
		CreatedAt:      timestamppb.New(p.CreatedAt),
	}
	if p.PaidAt != nil {
		result.PaidAt = timestamppb.New(*p.PaidAt)
	}
	return result
}

func toProtoMQJob(job *biz.MQJob) *pb.MQJobInfo {
	if job == nil {
		return nil
	}
	result := &pb.MQJobInfo{
		JobId:       job.ID,
		Kind:        job.Kind,
		Queue:       job.Queue,
		State:       job.State,
		Attempt:     int32(job.Attempt),
		MaxAttempts: int32(job.MaxAttempts),
		ArgsJson:    job.ArgsJSON,
		Tags:        job.Tags,
		Errors:      make([]*pb.MQJobError, len(job.Errors)),
	}
	if !job.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(job.CreatedAt)
	}
	if !job.ScheduledAt.IsZero() {
		result.ScheduledAt = timestamppb.New(job.ScheduledAt)
	}
	if job.AttemptedAt != nil && !job.AttemptedAt.IsZero() {
		result.AttemptedAt = timestamppb.New(*job.AttemptedAt)
	}
	if job.FinalizedAt != nil && !job.FinalizedAt.IsZero() {
		result.FinalizedAt = timestamppb.New(*job.FinalizedAt)
	}
	for i, err := range job.Errors {
		result.Errors[i] = &pb.MQJobError{
			Attempt: int32(err.Attempt),
			Error:   err.Error,
		}
		if !err.At.IsZero() {
			result.Errors[i].At = timestamppb.New(err.At)
		}
	}
	return result
}

func formatAmount(amountInFen int32) string {
	return strconv.FormatInt(int64(amountInFen), 10)
}

func toProtoTradeState(state biz.TradeState) pb.TradeState {
	switch state {
	case biz.TradeStateSuccess:
		return pb.TradeState_SUCCESS
	case biz.TradeStateRefund:
		return pb.TradeState_REFUND
	case biz.TradeStateNotPay:
		return pb.TradeState_NOTPAY
	case biz.TradeStateClosed:
		return pb.TradeState_CLOSED
	case biz.TradeStateRevoked:
		return pb.TradeState_REVOKED
	case biz.TradeStateUserPaying:
		return pb.TradeState_USERPAYING
	case biz.TradeStatePayError:
		return pb.TradeState_PAYERROR
	default:
		return pb.TradeState_TRADE_STATE_UNSPECIFIED
	}
}
