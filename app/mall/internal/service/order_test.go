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
	cancelledUser int64
}

func (u *orderServiceUsecase) CreateOrder(_ context.Context, req *biz.CreateOrderReq) (*biz.Order, error) {
	u.createReq = req
	return u.order, nil
}
func (u *orderServiceUsecase) GetOrder(context.Context, int64, int64) (*biz.Order, error) {
	return u.order, nil
}
func (u *orderServiceUsecase) ListOrders(context.Context, *biz.ListOrdersReq) ([]biz.Order, int64, error) {
	return []biz.Order{*u.order}, 1, nil
}
func (u *orderServiceUsecase) CancelOrder(_ context.Context, _ int64, userID int64) error {
	u.cancelledUser = userID
	return nil
}

func TestOrderService_AllOperationsUseAuthenticatedOwner(t *testing.T) {
	uc := &orderServiceUsecase{order: &biz.Order{ID: 1, UserID: 42, TotalAmount: 10000, Currency: "CNY", Items: []biz.OrderItem{{ProductID: 3, Quantity: 2, UnitPrice: 5000}}}}
	service := NewOrderService(uc)
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
	service := NewOrderService(uc)
	_, err := service.CreateOrder(authenticatedPaymentContext(42, "user"), &pb.CreateOrderRequest{
		AddressId: 1, IdempotencyKey: "  checkout-42  ", Items: []*pb.OrderItemInput{{ProductId: 1, Quantity: 1}},
	})
	require.NoError(t, err)
	require.Equal(t, "checkout-42", uc.createReq.IdempotencyKey)
}
