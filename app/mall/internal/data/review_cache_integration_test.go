//go:build integration

package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestReviewCacheInvalidationRecoveryIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID := fulfillmentOrder(t, f)
	payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	products := NewProductRepo(f.data, f.tx, log.DefaultLogger)
	orders := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
	payment, err := payments.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	_, refund, err := payments.PreparePaymentRefund(f.ctx, paymentID, payment.OutTradeNo+"-recovery")
	require.NoError(t, err)
	_, err = products.GetProduct(f.ctx, f.productID)
	require.NoError(t, err)
	_, err = products.ListProductsByCategory(f.ctx, f.categoryID, 20, 0)
	require.NoError(t, err)
	_, err = orders.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
	require.NoError(t, err)
	_, err = payments.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	// Isolate this mutation from seed events; the suite uses a dedicated DB.
	_, err = f.pool.Exec(f.ctx, `DELETE FROM cache_invalidations`)
	require.NoError(t, err)
	failedRedis := redis.NewClient(&redis.Options{Addr: f.rdb.Options().Addr, DB: 15})
	require.NoError(t, failedRedis.Close())
	failedData := *f.data
	failedData.rdb = failedRedis
	require.NoError(t, NewPaymentRepo(&failedData, f.tx, log.DefaultLogger).ApplyPaymentRefund(f.ctx, paymentID, refund.ID))
	stale, err := orders.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
	require.NoError(t, err)
	require.Len(t, stale, 1, "the warmed projection demonstrates the failed Redis write")
	require.Equal(t, orderID, stale[0].ID)
	var pending int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM cache_invalidations`).Scan(&pending))
	require.GreaterOrEqual(t, pending, 3, "payment/order/stock changes have durable targets")
	count, err := NewCacheInvalidationRepo(&failedData, f.tx, log.DefaultLogger).FlushCacheInvalidations(f.ctx, 1000)
	require.Error(t, err)
	require.Zero(t, count)
	var retained int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM cache_invalidations`).Scan(&retained))
	require.Equal(t, pending, retained)
	recovery := NewCacheInvalidationRepo(f.data, f.tx, log.DefaultLogger)
	count, err = recovery.FlushCacheInvalidations(f.ctx, 1000)
	require.NoError(t, err)
	require.Equal(t, pending, count)
	page, err := orders.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, page)
	product, err := products.GetProduct(f.ctx, f.productID)
	require.NoError(t, err)
	require.EqualValues(t, 100, product.Stock)
	category, err := products.ListProductsByCategory(f.ctx, f.categoryID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 100, category[0].Stock)
	payment, err = payments.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	require.Equal(t, biz.PaymentStatusRefunded, payment.Status)
	count, err = recovery.FlushCacheInvalidations(f.ctx, 1000)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestReviewCacheOutboxRollbackAndConcurrentDrainIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	_, err := f.pool.Exec(f.ctx, `DELETE FROM cache_invalidations`)
	require.NoError(t, err)
	wantErr := errors.New("abort mutation")
	err = f.tx.InTx(f.ctx, func(ctx context.Context) error {
		_, err := pgTxFromContext(ctx).Exec(ctx, `UPDATE products SET stock=stock-1 WHERE id=$1`, f.productID)
		if err != nil {
			return err
		}
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
	var pending int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM cache_invalidations`).Scan(&pending))
	require.Zero(t, pending, "rollback includes the database-triggered outbox fact")
	// Simulate a commit followed by immediate process death: no repository or
	// after-commit invalidation runs, yet its targets remain recoverable.
	_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=stock-1 WHERE id=$1`, f.productID)
	require.NoError(t, err)
	for range 19 {
		_, err = f.pool.Exec(f.ctx, `UPDATE products SET name=name WHERE id=$1`, f.productID)
		require.NoError(t, err)
	}
	type result struct {
		count int
		err   error
	}
	results := make(chan result, 4)
	start := make(chan struct{})
	for range 4 {
		go func() {
			<-start
			count, err := NewCacheInvalidationRepo(f.data, f.tx, log.DefaultLogger).FlushCacheInvalidations(f.ctx, 5)
			results <- result{count, err}
		}()
	}
	close(start)
	total := 0
	for range 4 {
		res := <-results
		require.NoError(t, res.err)
		total += res.count
	}
	require.Equal(t, 20, total, "SKIP LOCKED drainers each acknowledge their own rows")
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM cache_invalidations`).Scan(&pending))
	require.Zero(t, pending)
	gen, err := f.rdb.Get(f.ctx, "product:list:gen").Int64()
	require.NoError(t, err)
	require.EqualValues(t, 4, gen, "each five-row batch deduplicates its global list target")
	ctx, cancel := context.WithTimeout(f.ctx, time.Nanosecond)
	defer cancel()
	_, err = NewCacheInvalidationRepo(f.data, f.tx, log.DefaultLogger).FlushCacheInvalidations(ctx, 1000)
	require.Error(t, err)
}
