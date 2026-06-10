package service

import (
	"context"
	"io"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type PaymentService struct {
	pb.UnimplementedPaymentServer
	pb.UnimplementedWechatPayServiceServer
	paymentGateway biz.PaymentGateway
	paymentJobs *biz.PaymentJobUsecase
}

func NewPaymentService(paymentGateway biz.PaymentGateway, paymentJobs *biz.PaymentJobUsecase) *PaymentService {
	return &PaymentService{paymentGateway: paymentGateway, paymentJobs: paymentJobs}
}

func (s *PaymentService) CreatePayment(ctx context.Context, req *pb.CreatePaymentRequest) (*pb.PaymentInfo, error) {
	return &pb.PaymentInfo{}, nil
}
func (s *PaymentService) GetPayment(ctx context.Context, req *pb.GetPaymentRequest) (*pb.PaymentInfo, error) {
	return &pb.PaymentInfo{}, nil
}
func (s *PaymentService) GetPaymentByOrder(ctx context.Context, req *pb.GetPaymentByOrderRequest) (*pb.PaymentInfo, error) {
	return &pb.PaymentInfo{}, nil
}
func (s *PaymentService) NotifyPayment(ctx context.Context, req *pb.NotifyPaymentRequest) (*pb.NotifyPaymentReply, error) {
	return &pb.NotifyPaymentReply{}, nil
}
func (s *PaymentService) RefundPayment(ctx context.Context, req *pb.RefundPaymentRequest) (*pb.RefundPaymentReply, error) {
	return &pb.RefundPaymentReply{}, nil
}

func (s *PaymentService) CreateWechatPayCheckJob(ctx context.Context, req *pb.CreateWechatPayCheckJobRequest) (*pb.MQJobInfo, error) {
	if s.paymentJobs == nil {
		return nil, paymentMQMissing()
	}
	if req.DelaySeconds < 0 {
		return nil, errors.BadRequest("DELAY_SECONDS_INVALID", "delay_seconds must be greater than or equal to 0")
	}
	job, err := s.paymentJobs.EnqueueCheckWechatPay(ctx, biz.CheckWechatPayArgs{
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

func (s *PaymentService) PrepayJSAPI(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error) {
	if s.paymentGateway == nil {
		return nil, wechatPayProviderMissing()
	}
	result, err := s.paymentGateway.Prepay(ctx, biz.PaymentPrepayRequest{
		Channel:     string(biz.Wechat),
		OutTradeNo:  req.OutTradeNo,
		Description: req.Description,
		TotalAmount: req.TotalAmount,
		OpenID:      req.Openid,
	})
	if err != nil {
		return nil, err
	}
	return &pb.PrepayJSAPIReply{
		AppId:         result.AppID,
		TimeStamp:     result.TimeStamp,
		NonceStr:      result.NonceStr,
		PrepayPackage: result.Package,
		SignType:      result.SignType,
		PaySign:       result.PaySign,
	}, nil
}

func (s *PaymentService) QueryOrder(ctx context.Context, req *pb.QueryOrderRequest) (*pb.QueryOrderReply, error) {
	if s.paymentGateway == nil {
		return nil, wechatPayProviderMissing()
	}
	result, err := s.paymentGateway.QueryOrder(ctx, biz.PaymentQueryRequest{
		Channel:    string(biz.Wechat),
		OutTradeNo: req.OutTradeNo,
	})
	if err != nil {
		return nil, err
	}
	return &pb.QueryOrderReply{
		OutTradeNo:    result.OutTradeNo,
		TransactionId: result.TransactionID,
		TradeState:    toProtoTradeState(result.TradeState),
		TotalAmount:   result.TotalAmount,
	}, nil
}

func (s *PaymentService) CloseOrder(ctx context.Context, req *pb.CloseOrderRequest) (*pb.CloseOrderReply, error) {
	if s.paymentGateway == nil {
		return nil, wechatPayProviderMissing()
	}
	result, err := s.paymentGateway.CloseOrder(ctx, biz.PaymentCloseRequest{
		Channel:    string(biz.Wechat),
		OutTradeNo: req.OutTradeNo,
	})
	if err != nil {
		return nil, err
	}
	return &pb.CloseOrderReply{Success: result.Success}, nil
}

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

func wechatPayProviderMissing() error {
	return errors.ServiceUnavailable("WECHAT_PAY_NOT_CONFIGURED", "wechat pay gateway is not configured")
}

func paymentMQMissing() error {
	return errors.ServiceUnavailable("PAYMENT_MQ_NOT_CONFIGURED", "payment mq is not configured")
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
