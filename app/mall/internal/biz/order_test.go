package biz

import (
	"context"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type orderUsecaseRepo struct {
	created    CreateOrderArgs
	order      Order
	cancelUser int64
}

func TestHashOrderRequest_NormalizesItemOrder(t *testing.T) {
	first := []OrderItemInput{{ProductID: 9, Quantity: 1}, {ProductID: 3, Quantity: 2}}
	second := []OrderItemInput{{ProductID: 3, Quantity: 2}, {ProductID: 9, Quantity: 1}}
	uc := NewConfiguredOrderUsecase(&orderUsecaseRepo{}, paymentTestID{}, OrderPolicy{PaymentTimeout: time.Minute}, log.DefaultLogger)
	_, err := uc.CreateOrder(context.Background(), &CreateOrderReq{
		UserID: 1, AddressID: 2, IdempotencyKey: "checkout-normalized", Items: first,
	})
	require.NoError(t, err)
	repo := uc.(*orderUsecase).repo.(*orderUsecaseRepo)
	firstHash := repo.created.RequestHash
	_, err = uc.CreateOrder(context.Background(), &CreateOrderReq{
		UserID: 1, AddressID: 2, IdempotencyKey: "checkout-normalized", Items: second,
	})
	require.NoError(t, err)
	require.Equal(t, firstHash, repo.created.RequestHash)
	require.Equal(t, int64(3), repo.created.Items[0].ProductID)
}

func (r *orderUsecaseRepo) CreateOrder(_ context.Context, args CreateOrderArgs) (Order, error) {
	r.created = args
	r.order = Order{ID: 1, UserID: args.UserID, AddressID: args.AddressID, OutTradeNo: args.OutTradeNo, Currency: args.Currency}
	return r.order, nil
}
func (r *orderUsecaseRepo) GetOrder(context.Context, int64) (Order, error) { return r.order, nil }
func (r *orderUsecaseRepo) GetOrderByOrderNo(context.Context, string) (Order, error) {
	return r.order, nil
}
func (r *orderUsecaseRepo) GetOrderByUser(context.Context, int64, int64) (Order, error) {
	return r.order, nil
}
func (r *orderUsecaseRepo) HasOngoingOrders(context.Context, int64) (bool, error) { return false, nil }
func (r *orderUsecaseRepo) ListOngoingOrdersByUser(context.Context, int64) ([]Order, error) {
	return []Order{r.order}, nil
}
func (r *orderUsecaseRepo) ListOrdersByUser(context.Context, int64, int32, int32) ([]Order, error) {
	return []Order{r.order}, nil
}
func (r *orderUsecaseRepo) CountOrdersByUser(context.Context, int64) (int64, error) { return 1, nil }
func (r *orderUsecaseRepo) CancelOrderByUser(_ context.Context, _ int64, userID int64) error {
	r.cancelUser = userID
	return nil
}

func TestOrderUsecase_CreateUsesItemsAndServerOrderNumber(t *testing.T) {
	repo := &orderUsecaseRepo{}
	uc := NewOrderUsecase(repo, paymentTestID{}, log.DefaultLogger)
	order, err := uc.CreateOrder(context.Background(), &CreateOrderReq{UserID: 42, AddressID: 9, IdempotencyKey: "checkout-42", Items: []OrderItemInput{{ProductID: 3, Quantity: 2}}})
	require.NoError(t, err)
	require.Equal(t, int64(42), repo.created.UserID)
	require.Equal(t, "payment_99", repo.created.OutTradeNo)
	require.Equal(t, DefaultCurrency, repo.created.Currency)
	require.Len(t, repo.created.Items, 1)
	require.Equal(t, repo.created.OutTradeNo, order.OutTradeNo)
}

func TestOrderUsecase_RejectsInvalidAndDuplicateItems(t *testing.T) {
	uc := NewOrderUsecase(&orderUsecaseRepo{}, paymentTestID{}, log.DefaultLogger)
	_, err := uc.CreateOrder(context.Background(), &CreateOrderReq{UserID: 1, AddressID: 1, Items: []OrderItemInput{{ProductID: 2, Quantity: 0}}})
	require.Error(t, err)
	_, err = uc.CreateOrder(context.Background(), &CreateOrderReq{UserID: 1, AddressID: 1, Items: []OrderItemInput{{ProductID: 2, Quantity: 1}, {ProductID: 2, Quantity: 1}}})
	require.Error(t, err)
}
