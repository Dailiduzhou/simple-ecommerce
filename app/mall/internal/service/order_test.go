package service

import (
	"context"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/stretchr/testify/require"
)

type orderServiceUsecase struct {
	createReq     *biz.CreateOrderReq
	order         *biz.Order
	readActor     biz.Actor
	listReq       *biz.ListOrdersReq
	readCalls     int
	cancelledUser int64
}

func (u *orderServiceUsecase) CreateOrder(_ context.Context, req *biz.CreateOrderReq) (*biz.Order, error) {
	u.createReq = req
	return u.order, nil
}
func (u *orderServiceUsecase) GetOrder(_ context.Context, _ int64, actor biz.Actor) (*biz.Order, error) {
	u.readActor = actor
	u.readCalls++
	return u.order, nil
}
func (u *orderServiceUsecase) ListOrders(_ context.Context, actor biz.Actor, req *biz.ListOrdersReq) ([]biz.Order, int64, error) {
	u.readActor, u.listReq = actor, req
	u.readCalls++
	return []biz.Order{*u.order}, 1, nil
}
func (u *orderServiceUsecase) CancelOrder(_ context.Context, _ int64, userID int64) error {
	u.cancelledUser = userID
	return nil
}

func TestOrderService_AllOperationsUseAuthenticatedOwner(t *testing.T) {
	uc := &orderServiceUsecase{order: &biz.Order{ID: 1, UserID: 42, TotalAmount: 10000, Currency: "CNY", Items: []biz.OrderItem{{ProductID: 3, Quantity: 2, UnitPrice: 5000}}}}
	service := NewOrderService(uc, nil)
	ctx := authenticatedPaymentContext(42, "user")
	created, err := service.CreateOrder(ctx, &pb.CreateOrderRequest{AddressId: 9, IdempotencyKey: "checkout-42", Items: []*pb.OrderItemInput{{ProductId: 3, Quantity: 2}}})
	require.NoError(t, err)
	require.Equal(t, "100.00", created.TotalAmount)
	require.Equal(t, int64(42), uc.createReq.UserID)
	_, err = service.GetOrder(ctx, &pb.GetOrderRequest{Id: 1, UserId: 42})
	require.NoError(t, err)
	listed, err := service.ListOrders(ctx, &pb.ListOrdersRequest{UserId: 42})
	require.NoError(t, err)
	require.Len(t, listed.Orders, 1)
	_, err = service.CancelOrder(ctx, &pb.CancelOrderRequest{Id: 1, UserId: 42})
	require.NoError(t, err)
	require.Equal(t, int64(42), uc.cancelledUser)
}

func TestOrderService_PassesTrimmedIdempotencyKeyToUsecase(t *testing.T) {
	// Key validation lives in the biz layer; the service only forwards the
	// trimmed value so the two layers cannot drift apart.
	uc := &orderServiceUsecase{order: &biz.Order{}}
	service := NewOrderService(uc, nil)
	_, err := service.CreateOrder(authenticatedPaymentContext(42, "user"), &pb.CreateOrderRequest{
		AddressId: 1, IdempotencyKey: "  checkout-42  ", Items: []*pb.OrderItemInput{{ProductId: 1, Quantity: 1}},
	})
	require.NoError(t, err)
	require.Equal(t, "checkout-42", uc.createReq.IdempotencyKey)
}

func TestOrderService_AdminReadsBuyerOrdersWithoutImpersonation(t *testing.T) {
	uc := &orderServiceUsecase{order: &biz.Order{ID: 7, UserID: 42,
		Shipping: &biz.OrderShippingSnapshot{ReceiverName: "buyer", ReceiverPhone: "13800138000", DetailAddress: "checkout address"}}}
	s := NewOrderService(uc, nil)
	ctx := authenticatedPaymentContext(99, "admin")
	for _, userID := range []int64{0, 42} {
		order, err := s.GetOrder(ctx, &pb.GetOrderRequest{Id: 7, UserId: userID})
		require.NoError(t, err)
		require.Equal(t, "13800138000", order.Shipping.ReceiverPhone)
		require.Equal(t, "checkout address", order.Shipping.DetailAddress)
		require.Equal(t, biz.Actor{ID: 99, Admin: true}, uc.readActor)
	}
	_, err := s.GetOrder(ctx, &pb.GetOrderRequest{Id: 7, UserId: 43})
	require.ErrorIs(t, err, biz.ErrOrderNotFound, "explicit owner remains a filter")
	for _, ongoing := range []bool{false, true} {
		_, err = s.ListOrders(ctx, &pb.ListOrdersRequest{UserId: 42, Page: 2, PageSize: 5, Ongoing: ongoing})
		require.NoError(t, err)
		require.Equal(t, biz.Actor{ID: 99, Admin: true}, uc.readActor)
		require.Equal(t, &biz.ListOrdersReq{UserID: 42, Limit: 5, Offset: 5, Ongoing: ongoing}, uc.listReq)
	}
	_, err = s.ListOrders(ctx, &pb.ListOrdersRequest{})
	require.NoError(t, err)
	require.EqualValues(t, 99, uc.listReq.UserID, "omitted owner retains the current-user default")
	_, err = s.CancelOrder(ctx, &pb.CancelOrderRequest{Id: 7, UserId: 42})
	require.Error(t, err, "read access must not grant cancellation on behalf of a buyer")
	require.Zero(t, uc.cancelledUser)
}

func TestOrderService_RejectsForeignOwnerReadsBeforeUsecase(t *testing.T) {
	uc := &orderServiceUsecase{}
	s := NewOrderService(uc, nil)
	for _, ctx := range []context.Context{context.Background(), authenticatedPaymentContext(99, "user")} {
		_, err := s.GetOrder(ctx, &pb.GetOrderRequest{Id: 7, UserId: 42})
		require.Error(t, err)
		_, err = s.ListOrders(ctx, &pb.ListOrdersRequest{UserId: 42})
		require.Error(t, err)
	}
	require.Zero(t, uc.readCalls)
}
