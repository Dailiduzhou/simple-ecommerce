//go:build integration

package data

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestReviewContentEditsAndStockConservationIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	products := NewProductRepo(f.data, f.tx, log.DefaultLogger)
	orders := NewOrderRepoWithJobs(f.data, f.tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
	create := func(index int) (biz.Order, error) {
		return orders.CreateOrder(f.ctx, biz.CreateOrderArgs{UserID: f.userID, AddressID: f.addressID, Currency: "CNY",
			OutTradeNo: fmt.Sprintf("%s_order_%d", f.prefix, index), IdempotencyKey: fmt.Sprintf("%s_key_%d", f.prefix, index), RequestHash: f.prefix,
			PaymentTimeout: time.Hour, Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}}})
	}
	edit := func() error {
		_, err := products.UpdateProduct(f.ctx, f.productID, f.categoryID, "edited", decimal.RequireFromString("123.45"), decimal.NewFromInt(1), nil, nil, "content")
		return err
	}
	// Reproduce the stale form sequence: preload stock=100, reserve one, edit.
	view, err := products.GetProduct(f.ctx, f.productID)
	require.NoError(t, err)
	require.EqualValues(t, 100, view.Stock)
	order, err := create(0)
	require.NoError(t, err)
	require.NoError(t, edit())
	var stock int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 99, stock)
	require.NoError(t, orders.CancelOrderByUser(f.ctx, order.ID, f.userID))
	// Interleave concurrent editor and checkout transactions on the same row.
	var wg sync.WaitGroup
	start := make(chan struct{})
	type result struct {
		order biz.Order
		err   error
	}
	results := make(chan result, 16)
	for i := 1; i <= 8; i++ {
		wg.Add(2)
		go func(index int) { defer wg.Done(); <-start; order, err := create(index); results <- result{order, err} }(i)
		go func() { defer wg.Done(); <-start; results <- result{err: edit()} }()
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		if result.order.ID > 0 {
			require.NoError(t, orders.CancelOrderByUser(f.ctx, result.order.ID, f.userID))
		}
	}
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 100, stock)
	// Competing replays of one adjustment result in exactly one movement.
	input := biz.StockAdjustmentInput{ProductID: f.productID, ActorID: f.userID, Delta: 5, Reason: "restock", IdempotencyKey: f.prefix + "_adjust"}
	adjustments := make(chan *biz.StockAdjustment, 8)
	failures := make(chan error, 8)
	start = make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row, err := products.AdjustStock(f.ctx, input)
			adjustments <- row
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(adjustments)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var adjustmentID int64
	for row := range adjustments {
		if adjustmentID == 0 {
			adjustmentID = row.ID
		}
		require.Equal(t, adjustmentID, row.ID)
		require.EqualValues(t, 105, row.ResultingStock)
	}
	input.Delta = 6
	_, err = products.AdjustStock(f.ctx, input)
	require.ErrorIs(t, err, biz.ErrIdempotencyKeyConflict)
	input.IdempotencyKey += "_negative"
	input.Delta = -106
	_, err = products.AdjustStock(f.ctx, input)
	require.Error(t, err)
	var ledgerRows int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM stock_adjustments WHERE product_id=$1`, f.productID).Scan(&ledgerRows))
	require.Equal(t, 1, ledgerRows)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 105, stock)
}

func TestReviewCatalogConstraintsIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	products := NewProductRepo(f.data, f.tx, log.DefaultLogger)
	for _, status := range []int32{-1, 2, 65537} {
		require.Error(t, products.UpdateProductStatus(f.ctx, f.productID, status))
		_, err := f.pool.Exec(f.ctx, `UPDATE products SET status=$2 WHERE id=$1`, f.productID, status)
		require.Error(t, err)
	}
	var status int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM products WHERE id=$1`, f.productID).Scan(&status))
	require.Equal(t, 1, status)
	require.ErrorIs(t, products.UpdateProductStatus(f.ctx, -1, 1), biz.ErrProductNotFound)
}

func TestReviewOngoingPageBoundsIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO orders(user_id,address_id,total_amount_minor,out_trade_no,idempotency_key,request_hash,expires_at,
			receiver_name,receiver_phone_encrypt,shipping_province,shipping_city,shipping_district,shipping_detail_address)
		SELECT $1,$2,12345,$3||n::text,$3||n::text,'hash',now()+interval '1 hour',
			a.receiver_name,a.receiver_phone_encrypt,a.province,a.city,a.district,a.detail_address
		FROM generate_series(1,205) n CROSS JOIN shipping_addresses a WHERE a.id=$2`, f.userID, f.addressID, f.prefix)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_minor,product_name_snapshot)
		SELECT id,$2,1,12345,'snapshot' FROM orders WHERE user_id=$1`, f.userID, f.productID)
	require.NoError(t, err)
	repo := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
	page, err := repo.ListOngoingOrdersByUser(f.ctx, f.userID, 100, 0)
	require.NoError(t, err)
	require.Len(t, page, 100)
	for _, order := range page {
		require.Len(t, order.Items, 1)
	}
	last, err := repo.ListOngoingOrdersByUser(f.ctx, f.userID, 10, 201)
	require.NoError(t, err)
	require.Len(t, last, 4)
	count, err := repo.CountOngoingOrdersByUser(f.ctx, f.userID)
	require.NoError(t, err)
	require.EqualValues(t, 205, count)
	_, err = repo.ListOngoingOrdersByUser(f.ctx, f.userID, 101, 0)
	require.Error(t, err)
	_, err = repo.ListOngoingOrdersByUser(f.ctx, f.userID, 20, -1)
	require.Error(t, err)
}
