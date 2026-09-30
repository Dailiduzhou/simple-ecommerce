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
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestOrderPageLoadsItemsInOneBatch(t *testing.T) {
	for _, count := range []int{1, 20, 100} {
		q := mockdb.NewMockQuerier(gomock.NewController(t))
		rows := make([]db.Order, count)
		ids := make([]int64, count)
		items := make([]db.OrderItem, count)
		for i := range rows {
			ids[i] = int64(i + 1)
			rows[i] = db.Order{ID: ids[i], UserID: 7}
			items[i] = db.OrderItem{OrderID: ids[i], ProductID: 42, Quantity: 1}
		}
		q.EXPECT().ListOrdersByUser(gomock.Any(), db.ListOrdersByUserParams{UserID: 7, Limit: int32(count)}).Return(rows, nil).Times(1)
		q.EXPECT().ListOrderItemsByOrderIDs(gomock.Any(), ids).Return(items, nil).Times(1)
		repo := NewOrderRepo(newTestData(t, q, miniredis.RunT(t)), nil, log.DefaultLogger)
		page, err := repo.ListOrdersByUser(context.Background(), 7, int32(count), 0)
		require.NoError(t, err)
		require.Len(t, page, count)
		for _, order := range page {
			require.Len(t, order.Items, 1)
		}
	}
}

func TestCatalogDataRejectsInvalidStateBeforeQuery(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	d := newTestData(t, q, miniredis.RunT(t))
	products := NewProductRepo(d, nil, log.DefaultLogger)
	events := NewEventRepo(d, log.DefaultLogger)
	for _, status := range []int32{-1, 65537} {
		require.Error(t, products.UpdateProductStatus(context.Background(), 42, status))
		require.Error(t, events.UpdateEventStatus(context.Background(), 42, status))
		_, err := events.ListEvents(context.Background(), &status, 20, 0)
		require.Error(t, err)
	}
	q.EXPECT().GetProduct(gomock.Any(), int64(42)).Return(db.Product{}, pgx.ErrNoRows)
	require.ErrorIs(t, products.UpdateProductStatus(context.Background(), 42, 1), biz.ErrProductNotFound)
	q.EXPECT().UpdateEventStatus(gomock.Any(), gomock.Any()).Return(db.Event{}, pgx.ErrNoRows)
	require.Error(t, events.UpdateEventStatus(context.Background(), 42, 1))
}
