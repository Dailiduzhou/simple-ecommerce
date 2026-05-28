package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
)

type OrderService struct {
	pb.UnimplementedOrderServer
}

func NewOrderService() *OrderService {
	return &OrderService{}
}

func (s *OrderService) CreateOrder(ctx context.Context, req *pb.CreateOrderRequest) (*pb.OrderInfo, error) {
    return &pb.OrderInfo{}, nil
}
func (s *OrderService) GetOrder(ctx context.Context, req *pb.GetOrderRequest) (*pb.OrderInfo, error) {
    return &pb.OrderInfo{}, nil
}
func (s *OrderService) ListOrders(ctx context.Context, req *pb.ListOrdersRequest) (*pb.ListOrdersReply, error) {
    return &pb.ListOrdersReply{}, nil
}
func (s *OrderService) CancelOrder(ctx context.Context, req *pb.CancelOrderRequest) (*pb.CancelOrderReply, error) {
    return &pb.CancelOrderReply{}, nil
}
