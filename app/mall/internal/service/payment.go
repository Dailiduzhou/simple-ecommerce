package service

import (
	"context"
	"io"
	"strconv"
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
	paymentUc  biz.PaymentUsecase
	paymentJobs biz.PaymentJobUsecase
}

func NewPaymentService(paymentUc biz.PaymentUsecase, paymentJobs biz.PaymentJobUsecase) *PaymentService {
	return &PaymentService{
		paymentUc:  paymentUc,
		paymentJobs: paymentJobs,
	}
}

func (s *PaymentService) CreatePayment(ctx context.Context, req *pb.CreatePaymentRequest) (*pb.PaymentInfo, error) {
	payment, err := s.paymentUc.CreatePayment(ctx, req.OrderId, req.UserId, req.MerchantId, req.PayChannel)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

func (s *PaymentService) GetPayment(ctx context.Context, req *pb.GetPaymentRequest) (*pb.PaymentInfo, error) {
	payment, err := s.paymentUc.GetPayment(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

func (s *PaymentService) GetPaymentByOrder(ctx context.Context, req *pb.GetPaymentByOrderRequest) (*pb.PaymentInfo, error) {
	payment, err := s.paymentUc.GetPaymentByOrder(ctx, req.OrderId)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

func (s *PaymentService) NotifyPayment(ctx context.Context, req *pb.NotifyPaymentRequest) (*pb.NotifyPaymentReply, error) {
	return &pb.NotifyPaymentReply{Result: "success"}, nil
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
	result, err := s.paymentUc.Prepay(ctx, biz.PaymentPrepayRequest{
		Channel:     req.PayChannel,
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
	result, err := s.paymentUc.QueryOrder(ctx, biz.PaymentQueryRequest{
		Channel:    req.PayChannel,
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
	result, err := s.paymentUc.CloseOrder(ctx, biz.PaymentCloseRequest{
		Channel:    req.PayChannel,
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
		Kind:         job.Kind,
		Queue:        job.Queue,
		State:        job.State,
		Attempt:      int32(job.Attempt),
		MaxAttempts:  int32(job.MaxAttempts),
		ArgsJson:     job.ArgsJSON,
		Tags:         job.Tags,
		Errors:       make([]*pb.MQJobError, len(job.Errors)),
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
