package service

import (
	"context"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
)

type fulfillmentServiceRepo struct {
	actor biz.Actor
	input biz.OrderFulfillmentInput
	calls int
}

func (r *fulfillmentServiceRepo) ApplyFulfillment(_ context.Context, a biz.Actor, input biz.OrderFulfillmentInput) (*biz.OrderFulfillmentAction, error) {
	r.actor, r.input = a, input
	r.calls++
	return &biz.OrderFulfillmentAction{ID: 1, OrderID: input.OrderID, ActorID: a.ID, Action: input.Action, TrackingNumber: input.TrackingNumber}, nil
}
func (r *fulfillmentServiceRepo) ListFulfillmentActions(_ context.Context, a biz.Actor, id int64) ([]biz.OrderFulfillmentAction, error) {
	r.actor = a
	r.calls++
	return []biz.OrderFulfillmentAction{{OrderID: id}}, nil
}

func TestOrderFulfillmentServiceAuthorizationAndActorBinding(t *testing.T) {
	repo := &fulfillmentServiceRepo{}
	s := NewOrderService(nil, biz.NewOrderFulfillmentUsecase(repo))
	ship := &pb.ShipOrderRequest{Id: 7, IdempotencyKey: "shipping-001", Reason: "sent", Carrier: "carrier", TrackingNumber: "number"}
	_, err := s.ShipOrder(context.Background(), ship)
	require.Equal(t, int32(401), errors.FromError(err).Code)
	_, err = s.ShipOrder(authenticatedPaymentContext(42, "user"), ship)
	require.Equal(t, int32(403), errors.FromError(err).Code)
	require.Zero(t, repo.calls)
	result, err := s.ShipOrder(authenticatedPaymentContext(42, "admin"), ship)
	require.NoError(t, err)
	require.Equal(t, biz.Actor{ID: 42, Admin: true}, repo.actor)
	require.Equal(t, int64(42), result.ActorId)
	require.Equal(t, biz.OrderActionShip, repo.input.Action)
	_, err = s.CompleteOrder(context.Background(), &pb.CompleteOrderRequest{Id: 7})
	require.Equal(t, int32(401), errors.FromError(err).Code)
	_, err = s.CompleteOrder(authenticatedPaymentContext(43, "user"), &pb.CompleteOrderRequest{Id: 7, IdempotencyKey: "complete-001", Reason: "received"})
	require.NoError(t, err)
	require.Equal(t, biz.Actor{ID: 43}, repo.actor, "repository authorizes the authenticated actor against the stored owner")
	require.Equal(t, biz.OrderActionComplete, repo.input.Action)
	_, err = s.ListOrderFulfillmentActions(context.Background(), &pb.ListOrderFulfillmentActionsRequest{Id: 7})
	require.Equal(t, int32(401), errors.FromError(err).Code)
	_, err = s.ListOrderFulfillmentActions(authenticatedPaymentContext(44, "admin"), &pb.ListOrderFulfillmentActionsRequest{Id: 7})
	require.NoError(t, err)
	require.Equal(t, biz.Actor{ID: 44, Admin: true}, repo.actor)
}
