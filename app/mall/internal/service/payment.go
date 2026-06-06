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
	wechatPay   biz.WechatPayProvider
	paymentJobs *biz.PaymentJobUsecase
}

func NewPaymentService(wechatPay biz.WechatPayProvider, paymentJobs *biz.PaymentJobUsecase) *PaymentService {
	return &PaymentService{wechatPay: wechatPay, paymentJobs: paymentJobs}
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
	if s.wechatPay == nil {
		return nil, wechatPayProviderMissing()
	}
	return s.wechatPay.PrepayJSAPI(ctx, req)
}

func (s *PaymentService) QueryOrder(ctx context.Context, req *pb.QueryOrderRequest) (*pb.QueryOrderReply, error) {
	if s.wechatPay == nil {
		return nil, wechatPayProviderMissing()
	}
	return s.wechatPay.QueryOrder(ctx, req.OutTradeNo)
}

func (s *PaymentService) CloseOrder(ctx context.Context, req *pb.CloseOrderRequest) (*pb.CloseOrderReply, error) {
	if s.wechatPay == nil {
		return nil, wechatPayProviderMissing()
	}
	return s.wechatPay.CloseOrder(ctx, req.OutTradeNo)
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
	return errors.ServiceUnavailable("WECHAT_PAY_NOT_CONFIGURED", "wechat pay provider is not configured")
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
