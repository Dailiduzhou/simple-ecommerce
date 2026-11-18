//go:build integration

package data

import (
	"context"
	"errors"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func orderProjectionLoaders(r *OrderRepo, buyer, merchant int64) map[string]func(context.Context) ([]biz.Order, error) {
	return map[string]func(context.Context) ([]biz.Order, error){
		"buyer all":     func(ctx context.Context) ([]biz.Order, error) { return r.ListOrdersByUser(ctx, buyer, 20, 0) },
		"buyer ongoing": func(ctx context.Context) ([]biz.Order, error) { return r.ListOngoingOrdersByUser(ctx, buyer, 20, 0) },
		"merchant all": func(ctx context.Context) ([]biz.Order, error) {
			return r.ListOrdersByMerchant(ctx, merchant, false, 20, 0)
		},
		"merchant ongoing": func(ctx context.Context) ([]biz.Order, error) {
			return r.ListOrdersByMerchant(ctx, merchant, true, 20, 0)
		},
		"admin all":     func(ctx context.Context) ([]biz.Order, error) { return r.ListAllOrders(ctx, false, 20, 0) },
		"admin ongoing": func(ctx context.Context) ([]biz.Order, error) { return r.ListAllOrders(ctx, true, 20, 0) },
	}
}

func TestOrderOutboxRollbackIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, _, _ := f.seedPayment(t, biz.PaymentStatusPending)
	_, err := f.pool.Exec(f.ctx, `DELETE FROM cache_invalidations`)
	require.NoError(t, err)
	want := errors.New("abort order mutation")
	err = f.tx.InTx(f.ctx, func(ctx context.Context) error {
		_, err := pgTxFromContext(ctx).Exec(ctx, `UPDATE orders SET status='paid' WHERE id=$1`, orderID)
		if err != nil {
			return err
		}
		return want
	})
	require.ErrorIs(t, err, want)
	var pending int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM cache_invalidations`).Scan(&pending))
	require.Zero(t, pending)
	var status string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status))
	require.Equal(t, biz.OrderStatusPendingPayment, status)
}

// Exercise the trigger independently of repository after-commit callbacks:
// every committed order fact must recover all projections after process loss.
// Business transition legality remains covered by payment/fulfillment tests.
func TestOrderOutboxRecoversAllScopesWithoutFastPathIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	// Isolate from previous fixtures' outbox backlog; a single bounded drain
	// should cover this mutation, not assume the entire suite has <1000 rows.
	_, err := f.pool.Exec(f.ctx, `DELETE FROM cache_invalidations`)
	require.NoError(t, err)
	orders := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
	loaders := orderProjectionLoaders(orders, f.userID, f.merchantID)
	for _, load := range loaders {
		rows, err := load(f.ctx)
		require.NoError(t, err)
		require.Empty(t, rows)
	}
	orderID, _, _ := f.seedPayment(t, biz.PaymentStatusPending)
	recover := NewCacheInvalidationRepo(f.data, f.tx, log.DefaultLogger)
	for _, status := range []string{biz.OrderStatusPendingPayment, biz.OrderStatusPaid, biz.OrderStatusShipped, biz.OrderStatusCompleted, biz.OrderStatusCancelling, biz.OrderStatusCancelled, biz.OrderStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			completed := status == biz.OrderStatusCompleted || status == biz.OrderStatusCancelled || status == biz.OrderStatusRefunded
			if status != biz.OrderStatusPendingPayment {
				_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status=$2, is_completed=$3 WHERE id=$1`, orderID, status, completed)
				require.NoError(t, err)
			}
			var targets []string
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT generation_keys FROM cache_invalidations WHERE $1 = ANY(generation_keys) ORDER BY id DESC LIMIT 1`, redisKey("order", "user", f.userID, "gen")).Scan(&targets))
			require.ElementsMatch(t, []string{redisKey("order", "user", f.userID, "gen"), redisKey("order", "user", "ongoing", f.userID, "gen"), redisKey("order", "merchant", f.merchantID, "gen"), "order:all:gen"}, targets)
			count, err := recover.FlushCacheInvalidations(f.ctx, 1000)
			require.NoError(t, err)
			require.Positive(t, count)
			for name, load := range loaders {
				rows, err := load(f.ctx)
				require.NoError(t, err, name)
				if completed && (name == "buyer ongoing" || name == "merchant ongoing" || name == "admin ongoing") {
					require.Empty(t, rows, name)
				} else {
					require.Len(t, rows, 1, name)
					require.Equal(t, status, rows[0].Status, name)
				}
			}
			count, err = recover.FlushCacheInvalidations(f.ctx, 1000)
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}
