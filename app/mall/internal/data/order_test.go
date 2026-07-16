package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestOrderRepo_CreateCalculatesDatabasePriceAndSnapshotsAtomically(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
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
	repo := NewOrderRepo(d, testTxManager{q: q}, log.DefaultLogger)
	order, err := repo.CreateOrder(context.Background(), biz.CreateOrderArgs{UserID: 42, AddressID: 9, OutTradeNo: "order_1", Currency: "CNY", Items: []biz.OrderItemInput{{ProductID: 3, Quantity: 2}}})
	require.NoError(t, err)
	require.Equal(t, int64(10000), order.TotalAmount)
	require.Equal(t, "server product", order.Items[0].ProductName)
	require.Equal(t, "1", d.rdb.Get(context.Background(), "order:user:42:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "order:user:ongoing:42:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:category:7:gen").Val())
}

func TestOrderRepo_CancelRestoresStockOnlyAfterCASAndRejectsPaidOrder(t *testing.T) {
	t.Run("unpaid", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		q := mockdb.NewMockQuerier(ctrl)
		redisServer := miniredis.RunT(t)
		order := db.Order{ID: 1, UserID: 42, Status: biz.OrderStatusPendingPayment, OutTradeNo: pgtype.Text{String: "order_1", Valid: true}}
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
