package service

import (
	"context"
	"strconv"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type OrderService struct {
	pb.UnimplementedOrderServer
	orderUc biz.OrderUsecase
}

func NewOrderService(orderUc biz.OrderUsecase) *OrderService { return &OrderService{orderUc: orderUc} }

func (s *OrderService) CreateOrder(ctx context.Context, req *pb.CreateOrderRequest) (*pb.OrderInfo, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	items := make([]biz.OrderItemInput, len(req.Items))
	for i, item := range req.Items {
		if item == nil {
			return nil, errors.BadRequest("ORDER_ITEM_INVALID", "order item is required")
		}
		items[i] = biz.OrderItemInput{ProductID: item.ProductId, Quantity: item.Quantity}
	}
	order, err := s.orderUc.CreateOrder(ctx, &biz.CreateOrderReq{UserID: claims.UserID, AddressID: req.AddressId, Items: items})
	if err != nil {
		return nil, err
	}
	return toProtoOrder(order), nil
}

func (s *OrderService) GetOrder(ctx context.Context, req *pb.GetOrderRequest) (*pb.OrderInfo, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	order, err := s.orderUc.GetOrder(ctx, req.Id, claims.UserID)
	if err != nil {
		return nil, err
	}
	return toProtoOrder(order), nil
}

func (s *OrderService) ListOrders(ctx context.Context, req *pb.ListOrdersRequest) (*pb.ListOrdersReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	page, size := req.Page, req.PageSize
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	orders, total, err := s.orderUc.ListOrders(ctx, &biz.ListOrdersReq{UserID: claims.UserID, Ongoing: req.Ongoing, Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return nil, err
	}
	reply := &pb.ListOrdersReply{Orders: make([]*pb.OrderInfo, len(orders)), Total: int32(total)}
	for i := range orders {
		reply.Orders[i] = toProtoOrder(&orders[i])
	}
	return reply, nil
}

func (s *OrderService) CancelOrder(ctx context.Context, req *pb.CancelOrderRequest) (*pb.CancelOrderReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	if err := s.orderUc.CancelOrder(ctx, req.Id, claims.UserID); err != nil {
		return nil, err
	}
	return &pb.CancelOrderReply{}, nil
}

func toProtoOrder(order *biz.Order) *pb.OrderInfo {
	if order == nil {
		return nil
	}
	result := &pb.OrderInfo{Id: order.ID, UserId: order.UserID, AddressId: order.AddressID,
		TotalAmount: minorAmountString(order.TotalAmount), Status: order.Status, IsCompleted: order.IsCompleted,
		CreatedAt: timestamppb.New(order.CreatedAt), UpdatedAt: timestamppb.New(order.UpdatedAt), Items: make([]*pb.OrderItem, len(order.Items)), OrderNo: order.OutTradeNo, Currency: order.Currency}
	for i, item := range order.Items {
		result.Items[i] = &pb.OrderItem{ProductId: item.ProductID, ProductName: item.ProductName, CoverImage: item.CoverImage, Quantity: item.Quantity, UnitPrice: minorAmountString(item.UnitPrice)}
	}
	return result
}

func minorAmountString(amount int64) string {
	negative := amount < 0
	if negative {
		amount = -amount
	}
	value := strconv.FormatInt(amount/100, 10) + "." + fmtTwoDigits(amount%100)
	if negative {
		return "-" + value
	}
	return value
}
func fmtTwoDigits(value int64) string {
	if value < 10 {
		return "0" + strconv.FormatInt(value, 10)
	}
	return strconv.FormatInt(value, 10)
}
