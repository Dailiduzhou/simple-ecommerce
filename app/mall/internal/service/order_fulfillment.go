package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *OrderService) ShipOrder(ctx context.Context, req *pb.ShipOrderRequest) (*pb.OrderFulfillmentAction, error) {
	claims, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	result, err := s.fulfillment.Apply(ctx, biz.Actor{ID: claims.UserID, Admin: true}, biz.OrderFulfillmentInput{
		OrderID: req.Id, Action: biz.OrderActionShip, IdempotencyKey: req.IdempotencyKey, Reason: req.Reason, Carrier: req.Carrier, TrackingNumber: req.TrackingNumber,
	})
	if err != nil {
		return nil, err
	}
	return fulfillmentProto(*result), nil
}

func (s *OrderService) CompleteOrder(ctx context.Context, req *pb.CompleteOrderRequest) (*pb.OrderFulfillmentAction, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	result, err := s.fulfillment.Apply(ctx, a, biz.OrderFulfillmentInput{
		OrderID: req.Id, Action: biz.OrderActionComplete, IdempotencyKey: req.IdempotencyKey, Reason: req.Reason,
	})
	if err != nil {
		return nil, err
	}
	return fulfillmentProto(*result), nil
}

func (s *OrderService) ListOrderFulfillmentActions(ctx context.Context, req *pb.ListOrderFulfillmentActionsRequest) (*pb.ListOrderFulfillmentActionsReply, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("ORDER_REQUEST_REQUIRED", "request is required")
	}
	rows, err := s.fulfillment.List(ctx, a, req.Id)
	if err != nil {
		return nil, err
	}
	result := &pb.ListOrderFulfillmentActionsReply{Actions: make([]*pb.OrderFulfillmentAction, len(rows))}
	for i, row := range rows {
		result.Actions[i] = fulfillmentProto(row)
	}
	return result, nil
}

func fulfillmentProto(a biz.OrderFulfillmentAction) *pb.OrderFulfillmentAction {
	return &pb.OrderFulfillmentAction{Id: a.ID, OrderId: a.OrderID, ActorId: a.ActorID, Action: a.Action,
		FromStatus: a.FromStatus, ToStatus: a.ToStatus, IdempotencyKey: a.IdempotencyKey, Reason: a.Reason,
		Carrier: a.Carrier, TrackingNumber: a.TrackingNumber, CreatedAt: timestamppb.New(a.CreatedAt)}
}
