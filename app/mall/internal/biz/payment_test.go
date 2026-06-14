package biz

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePaymentRepo struct {
	getActive func(ctx context.Context, orderID int64, channel string) (*PaymentDO, error)
	create    func(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error)
}

func (r *fakePaymentRepo) CreatePayment(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error) {
	return r.create(ctx, args)
}

func (r *fakePaymentRepo) GetPayment(ctx context.Context, id int64) (*PaymentDO, error) {
	return nil, nil
}

func (r *fakePaymentRepo) GetPaymentByOrder(ctx context.Context, orderID int64) (*PaymentDO, error) {
	return nil, nil
}

func (r *fakePaymentRepo) GetActivePaymentByOrderChannel(ctx context.Context, orderID int64, channel string) (*PaymentDO, error) {
	return r.getActive(ctx, orderID, channel)
}

type fakeOrderRepo struct {
	getOrder func(ctx context.Context, id int64) (Order, error)
}

func (r *fakeOrderRepo) GetOrder(ctx context.Context, id int64) (Order, error) {
	return r.getOrder(ctx, id)
}

func (r *fakeOrderRepo) CancelOrder(ctx context.Context, id int64) error { return nil }
func (r *fakeOrderRepo) CompleteOrder(ctx context.Context, id int64) error {
	return nil
}
func (r *fakeOrderRepo) CreateOrder(ctx context.Context, userID, addressID int64, amount int32) (Order, error) {
	return Order{}, nil
}
func (r *fakeOrderRepo) GetOrderByUser(ctx context.Context, id, userID int64) (Order, error) {
	return Order{}, nil
}
func (r *fakeOrderRepo) HasOngoingOrders(ctx context.Context, userID int64) (bool, error) {
	return false, nil
}
func (r *fakeOrderRepo) ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]Order, error) {
	return nil, nil
}
func (r *fakeOrderRepo) ListOrdersByUser(ctx context.Context, userID int64, limit, offset int32) ([]Order, error) {
	return nil, nil
}
func (r *fakeOrderRepo) UpdateOrderStatus(ctx context.Context, id int64, status string) (Order, error) {
	return Order{}, nil
}

type fakeIDGenerator struct {
	counter  atomic.Int64
	prefix   string
	generate func() string
}

func (g *fakeIDGenerator) GenerateString() string {
	if g.generate != nil {
		return g.generate()
	}
	return g.prefix + itoa(g.counter.Add(1))
}

func (g *fakeIDGenerator) GenerateOrderNo32(prefix string) string {
	if g.generate != nil {
		return g.generate()
	}
	return fmt.Sprintf("%-2s%014d%016d", prefix, g.counter.Add(1), g.counter.Add(1))
}

func (g *fakeIDGenerator) GenerateOrderNo64(prefix string, userID int64) string {
	if g.generate != nil {
		return g.generate()
	}
	return fmt.Sprintf("%-4s%014d%08d%019d%019d", prefix, g.counter.Add(1), userID, g.counter.Add(1), g.counter.Add(1))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func newPaymentUsecase(repo *fakePaymentRepo, orderRepo *fakeOrderRepo, gen IDGenerator) PaymentUsecase {
	return NewPaymentUsecase(nil, repo, orderRepo, gen, log.DefaultLogger)
}

func TestCreatePayment_GeneratesOutTradeNo(t *testing.T) {
	orderRepo := &fakeOrderRepo{
		getOrder: func(ctx context.Context, id int64) (Order, error) {
			return Order{ID: 10, TotalAmount: 9900}, nil
		},
	}
	repo := &fakePaymentRepo{
		getActive: func(ctx context.Context, orderID int64, channel string) (*PaymentDO, error) {
			return nil, pgx.ErrNoRows
		},
		create: func(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error) {
			assert.Equal(t, int64(10), args.OrderID)
			assert.Equal(t, "wechat", args.PayChannel)
			assert.Equal(t, int32(9900), args.Amount)
			assert.NotEmpty(t, args.OutTradeNo, "out_trade_no should be generated")
			return &PaymentDO{ID: 1, OrderID: args.OrderID, PayChannel: args.PayChannel, OutTradeNo: args.OutTradeNo, Status: "pending"}, nil
		},
	}
	gen := &fakeIDGenerator{prefix: "snow-"}
	uc := newPaymentUsecase(repo, orderRepo, gen)

	got, err := uc.CreatePayment(context.Background(), 10, 1, 2, "wechat")
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.ID)
	assert.Equal(t, "snow-1", got.OutTradeNo)
}

func TestCreatePayment_ReusesExistingActive(t *testing.T) {
	orderRepo := &fakeOrderRepo{
		getOrder: func(ctx context.Context, id int64) (Order, error) {
			return Order{ID: 10, TotalAmount: 9900}, nil
		},
	}
	repo := &fakePaymentRepo{
		getActive: func(ctx context.Context, orderID int64, channel string) (*PaymentDO, error) {
			return &PaymentDO{ID: 99, OrderID: 10, PayChannel: channel, OutTradeNo: "existing", Status: "pending"}, nil
		},
		create: func(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error) {
			t.Fatalf("Create should not be called when an active payment exists")
			return nil, nil
		},
	}
	gen := &fakeIDGenerator{prefix: "snow-"}
	uc := newPaymentUsecase(repo, orderRepo, gen)

	got, err := uc.CreatePayment(context.Background(), 10, 1, 2, "wechat")
	require.NoError(t, err)
	assert.Equal(t, int64(99), got.ID)
	assert.Equal(t, "existing", got.OutTradeNo)
}

func TestCreatePayment_AllowsRetryAfterFailed(t *testing.T) {
	orderRepo := &fakeOrderRepo{
		getOrder: func(ctx context.Context, id int64) (Order, error) {
			return Order{ID: 10, TotalAmount: 9900}, nil
		},
	}
	repo := &fakePaymentRepo{
		getActive: func(ctx context.Context, orderID int64, channel string) (*PaymentDO, error) {
			return nil, pgx.ErrNoRows
		},
		create: func(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error) {
			return &PaymentDO{ID: 100, OrderID: args.OrderID, OutTradeNo: args.OutTradeNo, PayChannel: args.PayChannel, Status: "pending"}, nil
		},
	}
	gen := &fakeIDGenerator{prefix: "snow-"}
	uc := newPaymentUsecase(repo, orderRepo, gen)

	got, err := uc.CreatePayment(context.Background(), 10, 1, 2, "alipay")
	require.NoError(t, err)
	assert.Equal(t, int64(100), got.ID)
	assert.Equal(t, "alipay", got.PayChannel)
}

func TestCreatePayment_PropagatesRepoError(t *testing.T) {
	wantErr := errors.New("db down")
	orderRepo := &fakeOrderRepo{
		getOrder: func(ctx context.Context, id int64) (Order, error) {
			return Order{ID: 10, TotalAmount: 9900}, nil
		},
	}
	repo := &fakePaymentRepo{
		getActive: func(ctx context.Context, orderID int64, channel string) (*PaymentDO, error) {
			return nil, pgx.ErrNoRows
		},
		create: func(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error) {
			return nil, wantErr
		},
	}
	gen := &fakeIDGenerator{prefix: "snow-"}
	uc := newPaymentUsecase(repo, orderRepo, gen)

	_, err := uc.CreatePayment(context.Background(), 10, 1, 2, "wechat")
	assert.ErrorIs(t, err, wantErr)
}

func TestResolveOutTradeNo_RejectsInvalidSupplied(t *testing.T) {
	uc := &paymentUsecase{
		idGen: &fakeIDGenerator{prefix: "snow-"},
		log:   log.NewHelper(log.DefaultLogger),
	}

	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"too long", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz012345", "OUT_TRADE_NO_TOO_LONG"},
		{"bad char space", "abc 123", "OUT_TRADE_NO_INVALID_CHARSET"},
		{"bad char dot", "abc.123", "OUT_TRADE_NO_INVALID_CHARSET"},
		{"bad char slash", "abc/123", "OUT_TRADE_NO_INVALID_CHARSET"},
		{"bad char unicode", "abc中文", "OUT_TRADE_NO_INVALID_CHARSET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.resolveOutTradeNo(context.Background(), tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestResolveOutTradeNo_AcceptsValidSupplied(t *testing.T) {
	uc := &paymentUsecase{
		idGen: &fakeIDGenerator{prefix: "snow-"},
		log:   log.NewHelper(log.DefaultLogger),
	}
	got, err := uc.resolveOutTradeNo(context.Background(), "client_123-abc")
	require.NoError(t, err)
	assert.Equal(t, "client_123-abc", got)
}

func TestResolveOutTradeNo_GeneratesWhenEmpty(t *testing.T) {
	uc := &paymentUsecase{
		idGen: &fakeIDGenerator{prefix: "snow-"},
		log:   log.NewHelper(log.DefaultLogger),
	}
	got, err := uc.resolveOutTradeNo(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "snow-1", got)
}
