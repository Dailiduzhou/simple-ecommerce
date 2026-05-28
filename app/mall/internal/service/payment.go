package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
)

type PaymentService struct {
	pb.UnimplementedPaymentServer
}

func NewPaymentService() *PaymentService {
	return &PaymentService{}
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
