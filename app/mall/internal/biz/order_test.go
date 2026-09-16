package biz

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type sequentialIDGen struct{ n int }

func (g *sequentialIDGen) GenerateString() string {
	g.n++
	return fmt.Sprintf("order-%d", g.n)
}
func (g *sequentialIDGen) GenerateOrderNo32(string) string        { return g.GenerateString() }
func (g *sequentialIDGen) GenerateOrderNo64(string, int64) string { return g.GenerateString() }

// retryOrderNoRepo fails the first len(errs) creates with the given errors.
type retryOrderNoRepo struct {
	orderUsecaseRepo
	attempts int
	errs     []error
}

func (r *retryOrderNoRepo) CreateOrder(_ context.Context, args CreateOrderArgs) (Order, error) {
	r.attempts++
	r.created = args
	if r.attempts <= len(r.errs) {
		return Order{}, r.errs[r.attempts-1]
	}
	return Order{ID: 1, UserID: args.UserID, AddressID: args.AddressID, OutTradeNo: args.OutTradeNo, Currency: args.Currency}, nil
}

func TestOrderUsecase_RetriesOnOrderNumberCollision(t *testing.T) {
	repo := &retryOrderNoRepo{errs: []error{ErrOrderNoCollision, ErrOrderNoCollision}}
	uc := NewOrderUsecase(repo, &sequentialIDGen{}, log.DefaultLogger)
	req := &CreateOrderReq{UserID: 7, AddressID: 3, IdempotencyKey: "checkout-retry", Items: []OrderItemInput{{ProductID: 1, Quantity: 1}}}

	order, err := uc.CreateOrder(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 3, repo.attempts, "two collisions must trigger two retries")
	require.Equal(t, "order-3", order.OutTradeNo, "each retry must mint a fresh number")

	// Once the attempts are exhausted the collision is surfaced instead of
	// looping forever.
	exhausted := &retryOrderNoRepo{errs: []error{ErrOrderNoCollision, ErrOrderNoCollision, ErrOrderNoCollision}}
	uc = NewOrderUsecase(exhausted, &sequentialIDGen{}, log.DefaultLogger)
	_, err = uc.CreateOrder(context.Background(), req)
	require.ErrorIs(t, err, ErrOrderNoCollision)
	require.Equal(t, 3, exhausted.attempts)
}

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

func TestOrderUsecase_ValidatesIdempotencyKey(t *testing.T) {
	uc := NewOrderUsecase(&orderUsecaseRepo{}, paymentTestID{}, log.DefaultLogger)
	items := []OrderItemInput{{ProductID: 2, Quantity: 1}}
	_, err := uc.CreateOrder(context.Background(), &CreateOrderReq{UserID: 1, AddressID: 1, Items: items})
	require.ErrorIs(t, err, ErrIdempotencyKeyRequired)
	_, err = uc.CreateOrder(context.Background(), &CreateOrderReq{UserID: 1, AddressID: 1, IdempotencyKey: "short", Items: items})
	require.ErrorIs(t, err, ErrIdempotencyKeyInvalid)
	_, err = uc.CreateOrder(context.Background(), &CreateOrderReq{UserID: 1, AddressID: 1, IdempotencyKey: strings.Repeat("k", 65), Items: items})
	require.ErrorIs(t, err, ErrIdempotencyKeyInvalid)
}
