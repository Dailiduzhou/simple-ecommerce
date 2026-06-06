package service

import (
	"context"
	"io"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

type PaymentService struct {
	pb.UnimplementedPaymentServer
	pb.UnimplementedWechatPayServiceServer
	wechatPay biz.WechatPayProvider
}

func NewPaymentService(wechatPay biz.WechatPayProvider) *PaymentService {
	return &PaymentService{wechatPay: wechatPay}
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
