//go:build integration

package data

import (
	"testing"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestAPIReportProductAndCategoryIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	products := NewProductRepo(f.data, f.tx, log.DefaultLogger)
	categories := NewCategoryRepo(f.data, log.DefaultLogger)
	require.ErrorIs(t, categories.DeleteCategory(f.ctx, f.categoryID), biz.ErrCategoryHasProducts)
	_, err := products.UpdateProduct(f.ctx, f.productID, -1, "x", decimal.NewFromInt(1), decimal.NewFromInt(1), nil, nil, "")
	require.ErrorIs(t, err, biz.ErrCategoryNotFound)
	require.NoError(t, products.SoftDeleteProduct(f.ctx, f.productID))
	var category pgtype.Int8
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT category_id FROM products WHERE id=$1`, f.productID).Scan(&category))
	require.False(t, category.Valid)
	require.ErrorIs(t, products.SoftDeleteProduct(f.ctx, f.productID), biz.ErrProductNotFound)
	_, err = products.UpdateProduct(f.ctx, f.productID, f.categoryID, "x", decimal.NewFromInt(1), decimal.NewFromInt(1), nil, nil, "")
	require.ErrorIs(t, err, biz.ErrProductNotFound)
	require.NoError(t, categories.DeleteCategory(f.ctx, f.categoryID))
	require.ErrorIs(t, categories.DeleteCategory(f.ctx, f.categoryID), biz.ErrCategoryNotFound)
}

func TestAPIReportUnavailableProductAndAddressDeleteIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	require.NoError(t, NewProductRepo(f.data, f.tx, log.DefaultLogger).UpdateProductStatus(f.ctx, f.productID, 0))
	orders := NewOrderRepoWithJobs(f.data, f.tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
	_, err := orders.CreateOrder(f.ctx, biz.CreateOrderArgs{UserID: f.userID, AddressID: f.addressID, Currency: "CNY", OutTradeNo: f.prefix, IdempotencyKey: f.prefix, RequestHash: f.prefix, PaymentTimeout: time.Hour, Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}}})
	require.True(t, userv1.IsProductNotAvailable(err))
	addresses := NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger)
	require.ErrorIs(t, addresses.DeleteShippingAddress(f.ctx, f.addressID, -1), biz.ErrAddressNotFound)
	var exists bool
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM shipping_addresses WHERE id=$1)`, f.addressID).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, addresses.DeleteShippingAddress(f.ctx, f.addressID, f.userID))
	require.ErrorIs(t, addresses.DeleteShippingAddress(f.ctx, f.addressID, f.userID), biz.ErrAddressNotFound)
}
