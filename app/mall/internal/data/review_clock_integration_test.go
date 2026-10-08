//go:build integration

package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestReviewPaymentWindowUsesDatabaseClockIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orders := NewOrderRepoWithJobs(f.data, f.tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
	var before, after time.Time
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT clock_timestamp()`).Scan(&before))
	order, err := orders.CreateOrder(f.ctx, biz.CreateOrderArgs{
		UserID: f.userID, AddressID: f.addressID, Currency: "CNY", OutTradeNo: f.prefix + "_clock",
		IdempotencyKey: f.prefix + "_clock", RequestHash: f.prefix, PaymentTimeout: 3 * time.Minute,
		Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}},
	})
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT clock_timestamp()`).Scan(&after))
	require.False(t, order.ExpiresAt.Before(before.Add(3*time.Minute)))
	require.False(t, order.ExpiresAt.After(after.Add(3*time.Minute)))
	var scheduled time.Time
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT scheduled_at FROM river_job WHERE kind='expire_order' AND (args->>'order_id')::bigint=$1`, order.ID).Scan(&scheduled))
	require.True(t, scheduled.Equal(order.ExpiresAt))

	payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	args := biz.CreatePaymentArgs{OrderID: order.ID, UserID: order.UserID, Amount: order.TotalAmount, Currency: order.Currency, Method: "alipay:wap", OutTradeNo: f.prefix + "_payment"}
	_, err = payments.CreatePayment(f.ctx, args)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, order.ID)
	require.NoError(t, err)
	_, err = payments.CreatePayment(f.ctx, args)
	require.ErrorIs(t, err, biz.ErrOrderExpired, "even replayed active payments obey the database deadline")

	// A long-running transaction must see wall time advancing, rather than
	// treating transaction start (CURRENT_TIMESTAMP) as the expiry clock.
	require.NoError(t, f.tx.InTx(f.ctx, func(ctx context.Context) error {
		raw := pgTxFromContext(ctx)
		_, err := raw.Exec(ctx, `UPDATE orders SET expires_at=clock_timestamp()+interval '50 milliseconds' WHERE id=$1`, order.ID)
		if err != nil {
			return err
		}
		_, err = raw.Exec(ctx, `SELECT pg_sleep(0.1)`)
		if err != nil {
			return err
		}
		expired, err := f.data.DB(ctx).OrderIsExpired(ctx, order.ID)
		require.NoError(t, err)
		require.True(t, expired)
		return err
	}))
}
