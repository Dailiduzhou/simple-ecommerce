package data

import (
	"context"
	"fmt"
	"testing"

	orderv1 "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestProductPersistenceErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		reason string
	}{
		{pgx.ErrNoRows, 404, "PRODUCT_NOT_FOUND"},
		{fmt.Errorf("wrapped: %w", pgx.ErrNoRows), 404, "PRODUCT_NOT_FOUND"},
		{&pgconn.PgError{Code: "23503", ConstraintName: "fk_product_category"}, 404, "CATEGORY_NOT_FOUND"},
		{&pgconn.PgError{Code: "23514", ConstraintName: "products_stock_check"}, 400, "PRODUCT_INVALID"},
	} {
		err := mapProductError(tc.err)
		require.Equal(t, tc.code, errors.Code(err))
		require.Equal(t, tc.reason, errors.Reason(err))
	}
	for _, operation := range []string{"create", "update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			d := newTestData(t, q, miniredis.RunT(t))
			repo := NewProductRepo(d, testTxManager{q: q}, log.DefaultLogger)
			var err error
			switch operation {
			case "create":
				q.EXPECT().CreateProduct(gomock.Any(), gomock.Any()).Return(db.Product{}, &pgconn.PgError{Code: "23503", ConstraintName: "fk_product_category"})
				_, err = repo.CreateProduct(context.Background(), 999, "x", decimal.NewFromInt(1), decimal.NewFromInt(1), 1, 1, nil, nil, "")
				require.ErrorIs(t, err, biz.ErrCategoryNotFound)
			case "update":
				q.EXPECT().GetProduct(gomock.Any(), int64(999)).Return(db.Product{}, pgx.ErrNoRows)
				_, err = repo.UpdateProduct(context.Background(), 999, 1, "x", decimal.NewFromInt(1), decimal.NewFromInt(1), nil, nil, "")
				require.ErrorIs(t, err, biz.ErrProductNotFound)
			case "delete":
				q.EXPECT().GetProduct(gomock.Any(), int64(999)).Return(db.Product{}, pgx.ErrNoRows)
				require.ErrorIs(t, repo.SoftDeleteProduct(context.Background(), 999), biz.ErrProductNotFound)
			}
		})
	}
}

func TestAddressDeleteMissIsNotFound(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	d := newTestData(t, q, miniredis.RunT(t))
	q.EXPECT().DeleteShippingAddress(gomock.Any(), db.DeleteShippingAddressParams{ID: 99, UserID: 1}).Return(int64(0), nil)
	repo := NewShippingAddressRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.ErrorIs(t, repo.DeleteShippingAddress(context.Background(), 99, 1), biz.ErrAddressNotFound)
}

func TestCancelAlreadyCancelledIsConflict(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	d := newTestData(t, q, miniredis.RunT(t))
	q.EXPECT().GetOrderByUserForUpdate(gomock.Any(), db.GetOrderByUserForUpdateParams{ID: 7, UserID: 1}).Return(db.Order{ID: 7, UserID: 1, Status: biz.OrderStatusCancelled}, nil)
	repo := NewOrderRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.True(t, orderv1.IsOrderAlreadyCancelled(repo.CancelOrderByUser(context.Background(), 7, 1)))
}
