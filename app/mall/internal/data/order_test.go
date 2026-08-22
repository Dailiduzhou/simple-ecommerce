package data

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type orderTestMQ struct {
	args biz.ExpireOrderArgs
	at   time.Time
	err  error
}

func (m *orderTestMQ) EnqueueExpireOrder(_ context.Context, args biz.ExpireOrderArgs, at time.Time) (*biz.MQJob, error) {
	m.args = args
	m.at = at
	if m.err != nil {
		return nil, m.err
	}
	return &biz.MQJob{ID: 1}, nil
}

func (m *orderTestMQ) EnqueueExpireOrderTx(_ context.Context, args biz.ExpireOrderArgs, at time.Time) (*biz.MQJob, error) {
	m.args = args
	m.at = at
	if m.err != nil {
		return nil, m.err
	}
	return &biz.MQJob{ID: 1}, nil
}

func TestOrderRepo_CreateCalculatesDatabasePriceAndSnapshotsAtomically(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows)
	q.EXPECT().GetShippingAddress(gomock.Any(), db.GetShippingAddressParams{ID: 9, UserID: 42}).Return(db.ShippingAddress{ID: 9, UserID: 42}, nil)
	product := db.Product{ID: 3, CategoryID: 7, Name: "server product", PriceMinor: 5000, Stock: 10, Status: 1, CoverImage: []byte(`[{"OssURL":"cover"}]`)}
	q.EXPECT().GetProductForOrder(gomock.Any(), int64(3)).Return(product, nil)
	q.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreateOrderParams) (db.Order, error) {
		require.Equal(t, int64(10000), args.TotalAmountMinor)
		require.Equal(t, "CNY", args.Currency)
		return db.Order{ID: 1, UserID: args.UserID, AddressID: args.AddressID, TotalAmountMinor: args.TotalAmountMinor, Currency: args.Currency, Status: biz.OrderStatusPendingPayment, OutTradeNo: args.OutTradeNo}, nil
	})
	q.EXPECT().DecrProductStock(gomock.Any(), db.DecrProductStockParams{ID: 3, Stock: 2}).Return(int32(8), nil)
	q.EXPECT().CreateOrderItem(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreateOrderItemParams) (db.OrderItem, error) {
		require.Equal(t, int64(5000), args.UnitPriceMinor)
		require.Equal(t, "server product", args.ProductNameSnapshot)
		require.JSONEq(t, string(product.CoverImage), string(args.CoverImageSnapshot))
		return db.OrderItem{OrderID: 1, ProductID: 3}, nil
	})
	d := newTestData(t, q, redisServer)
	mq := &orderTestMQ{}
	repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, mq, log.DefaultLogger)
	expiresAt := time.Now().Add(time.Minute)
	order, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
		UserID: 42, AddressID: 9, OutTradeNo: "order_1", Currency: "CNY",
		IdempotencyKey: "checkout-42", RequestHash: "hash", ExpiresAt: expiresAt,
		Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 2}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(10000), order.TotalAmount)
	require.Equal(t, "server product", order.Items[0].ProductName)
	require.Equal(t, "1", d.rdb.Get(context.Background(), "order:user:42:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "order:user:ongoing:42:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:category:7:gen").Val())
	require.Equal(t, biz.ExpireOrderArgs{OrderID: 1}, mq.args)
	require.Equal(t, expiresAt, mq.at)
}

func TestOrderRepo_CreateIdempotency(t *testing.T) {
	t.Run("same request returns existing order without inventory or cache-generation changes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		existing := db.Order{ID: 7, UserID: 42, AddressID: 9, Status: biz.OrderStatusPendingPayment,
			OutTradeNo: "original-order-no", IdempotencyKey: "checkout-42", RequestHash: "same-hash"}
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), db.GetOrderByUserIdempotencyParams{
			UserID: 42, IdempotencyKey: "checkout-42",
		}).Return(existing, nil)
		q.EXPECT().ListOrderItems(gomock.Any(), int64(7)).Return([]db.OrderItem{{
			OrderID: 7, ProductID: 3, Quantity: 2, UnitPriceMinor: 5000, ProductNameSnapshot: "snapshot",
		}}, nil)
		d := newTestData(t, q, redisServer)
		mq := &orderTestMQ{}
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, mq, log.DefaultLogger)

		order, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, OutTradeNo: "new-unused-order-no", Currency: "CNY",
			IdempotencyKey: "checkout-42", RequestHash: "same-hash", ExpiresAt: time.Now().Add(time.Minute),
			Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 2}},
		})

		require.NoError(t, err)
		require.Equal(t, int64(7), order.ID)
		require.Equal(t, "original-order-no", order.OutTradeNo)
		require.Zero(t, mq.args.OrderID)
		require.Equal(t, "", d.rdb.Get(context.Background(), "order:user:42:gen").Val())
		require.Equal(t, "", d.rdb.Get(context.Background(), "product:list:gen").Val())
	})

	t.Run("same key with a different request hash conflicts", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{RequestHash: "first-hash"}, nil)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{}, log.DefaultLogger)

		_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, IdempotencyKey: "checkout-42", RequestHash: "second-hash",
		})

		require.ErrorIs(t, err, biz.ErrIdempotencyKeyConflict)
	})
}

func TestOrderRepo_CreateMapsConcurrentConstraintErrors(t *testing.T) {
	t.Run("concurrent idempotency winner is returned", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows)
		q.EXPECT().GetShippingAddress(gomock.Any(), gomock.Any()).Return(db.ShippingAddress{ID: 9, UserID: 42}, nil)
		q.EXPECT().GetProductForOrder(gomock.Any(), int64(3)).Return(db.Product{ID: 3, PriceMinor: 5000, Stock: 10, Status: 1}, nil)
		q.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).Return(db.Order{}, &pgconn.PgError{
			Code: "23505", ConstraintName: "idx_orders_user_idempotency",
		})
		existing := db.Order{ID: 8, UserID: 42, AddressID: 9, OutTradeNo: "winner", RequestHash: "same-hash"}
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(existing, nil)
		q.EXPECT().ListOrderItems(gomock.Any(), int64(8)).Return(nil, nil)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{}, log.DefaultLogger)

		order, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, OutTradeNo: "loser", Currency: "CNY",
			IdempotencyKey: "checkout-42", RequestHash: "same-hash", ExpiresAt: time.Now().Add(time.Minute),
			Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 2}},
		})

		require.NoError(t, err)
		require.Equal(t, int64(8), order.ID)
		require.Equal(t, "winner", order.OutTradeNo)
	})

	t.Run("address deleted after ownership check remains a not-found error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows)
		q.EXPECT().GetShippingAddress(gomock.Any(), gomock.Any()).Return(db.ShippingAddress{ID: 9, UserID: 42}, nil)
		q.EXPECT().GetProductForOrder(gomock.Any(), int64(3)).Return(db.Product{ID: 3, PriceMinor: 5000, Stock: 10, Status: 1}, nil)
		q.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).Return(db.Order{}, &pgconn.PgError{
			Code: "23503", ConstraintName: "fk_order_address",
		})
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{}, log.DefaultLogger)

		_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, Currency: "CNY", IdempotencyKey: "checkout-42", RequestHash: "hash",
			Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 1}},
		})

		require.ErrorIs(t, err, biz.ErrAddressNotFound)
	})
}

func TestOrderRepo_CreateRejectsInvalidAmountAndPropagatesAtomicFailures(t *testing.T) {
	t.Run("zero total is a domain conflict instead of a postgres check violation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows)
		q.EXPECT().GetShippingAddress(gomock.Any(), gomock.Any()).Return(db.ShippingAddress{ID: 9, UserID: 42}, nil)
		q.EXPECT().GetProductForOrder(gomock.Any(), int64(3)).Return(db.Product{ID: 3, PriceMinor: 0, Stock: 10, Status: 1}, nil)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{}, log.DefaultLogger)

		_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, Currency: "CNY", IdempotencyKey: "checkout-42", RequestHash: "hash",
			Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 1}},
		})

		require.ErrorIs(t, err, biz.ErrOrderAmountInvalid)
	})

	t.Run("inventory compare-and-set failure maps to insufficient stock", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows)
		q.EXPECT().GetShippingAddress(gomock.Any(), gomock.Any()).Return(db.ShippingAddress{ID: 9, UserID: 42}, nil)
		q.EXPECT().GetProductForOrder(gomock.Any(), int64(3)).Return(db.Product{ID: 3, PriceMinor: 5000, Stock: 10, Status: 1}, nil)
		q.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).Return(db.Order{ID: 1}, nil)
		q.EXPECT().DecrProductStock(gomock.Any(), gomock.Any()).Return(int32(0), pgx.ErrNoRows)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{}, log.DefaultLogger)

		_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, Currency: "CNY", IdempotencyKey: "checkout-42", RequestHash: "hash",
			Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 1}},
		})

		require.ErrorIs(t, err, biz.ErrInsufficientStock)
	})

	t.Run("river insertion failure aborts the operation and skips cache publication", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), gomock.Any()).Return(db.Order{}, pgx.ErrNoRows)
		q.EXPECT().GetShippingAddress(gomock.Any(), gomock.Any()).Return(db.ShippingAddress{ID: 9, UserID: 42}, nil)
		q.EXPECT().GetProductForOrder(gomock.Any(), int64(3)).Return(db.Product{ID: 3, CategoryID: 7, PriceMinor: 5000, Stock: 10, Status: 1}, nil)
		q.EXPECT().CreateOrder(gomock.Any(), gomock.Any()).Return(db.Order{ID: 1, UserID: 42}, nil)
		q.EXPECT().DecrProductStock(gomock.Any(), gomock.Any()).Return(int32(9), nil)
		q.EXPECT().CreateOrderItem(gomock.Any(), gomock.Any()).Return(db.OrderItem{}, nil)
		d := newTestData(t, q, redisServer)
		insertErr := stderrors.New("river unavailable")
		repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{err: insertErr}, log.DefaultLogger)

		_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
			UserID: 42, AddressID: 9, Currency: "CNY", IdempotencyKey: "checkout-42", RequestHash: "hash",
			Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 1}}, ExpiresAt: time.Now().Add(time.Minute),
		})

		require.ErrorIs(t, err, insertErr)
		require.Equal(t, int64(0), d.rdb.DBSize(context.Background()).Val())
	})
}

func TestOrderRepo_CancelRestoresStockOnlyAfterCASAndRejectsPaidOrder(t *testing.T) {
	t.Run("unpaid", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		order := db.Order{ID: 1, UserID: 42, Status: biz.OrderStatusPendingPayment, OutTradeNo: "order_1"}
		q.EXPECT().GetOrderByUserForUpdate(gomock.Any(), db.GetOrderByUserForUpdateParams{ID: 1, UserID: 42}).Return(order, nil)
		q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(1)).Return(nil, nil)
		q.EXPECT().MarkOrderCancelling(gomock.Any(), int64(1)).Return(order, nil)
		q.EXPECT().RestoreOrderItemStock(gomock.Any(), int64(1)).Return(nil)
		q.EXPECT().MarkOrderCancelled(gomock.Any(), int64(1)).Return(order, nil)
		q.EXPECT().ListOrderItems(gomock.Any(), int64(1)).Return(nil, nil)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepo(d, testTxManager{q: q}, log.DefaultLogger)
		require.NoError(t, repo.CancelOrderByUser(context.Background(), 1, 42))
	})
	t.Run("paid", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		order := db.Order{ID: 1, UserID: 42, Status: biz.OrderStatusPendingPayment}
		q.EXPECT().GetOrderByUserForUpdate(gomock.Any(), db.GetOrderByUserForUpdateParams{ID: 1, UserID: 42}).Return(order, nil)
		q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(1)).Return([]db.Payment{{ID: 2, OrderID: 1, Status: biz.PaymentStatusSuccess}}, nil)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepo(d, testTxManager{q: q}, log.DefaultLogger)
		require.ErrorIs(t, repo.CancelOrderByUser(context.Background(), 1, 42), biz.ErrOrderAlreadyPaid)
	})
	t.Run("active payment", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		order := db.Order{ID: 1, UserID: 42, Status: biz.OrderStatusPendingPayment}
		q.EXPECT().GetOrderByUserForUpdate(gomock.Any(), db.GetOrderByUserForUpdateParams{ID: 1, UserID: 42}).Return(order, nil)
		q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(1)).Return([]db.Payment{{ID: 2, OrderID: 1, Status: biz.PaymentStatusPending}}, nil)
		d := newTestData(t, q, redisServer)
		repo := NewOrderRepo(d, testTxManager{q: q}, log.DefaultLogger)
		require.ErrorIs(t, repo.CancelOrderByUser(context.Background(), 1, 42), biz.ErrOrderHasActivePayment)
	})
}

func TestOrderRepo_ReplayAgainstCancelledOrderRequiresNewKey(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	cancelled := db.Order{ID: 7, UserID: 42, Status: biz.OrderStatusCancelled,
		IdempotencyKey: "checkout-42", RequestHash: "same-hash"}
	q.EXPECT().GetOrderByUserIdempotency(gomock.Any(), db.GetOrderByUserIdempotencyParams{
		UserID: 42, IdempotencyKey: "checkout-42",
	}).Return(cancelled, nil)
	d := newTestData(t, q, redisServer)
	repo := NewOrderRepoWithJobs(d, testTxManager{q: q}, &orderTestMQ{}, log.DefaultLogger)
	_, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{
		UserID: 42, AddressID: 9, IdempotencyKey: "checkout-42", RequestHash: "same-hash",
		Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 2}},
	})
	require.ErrorIs(t, err, biz.ErrIdempotencyKeyReused)
}
