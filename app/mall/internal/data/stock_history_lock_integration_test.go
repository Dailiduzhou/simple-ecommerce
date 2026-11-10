//go:build integration

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type stockHistoryLockQuerier struct{ orderUserLockQuerier }

func (q stockHistoryLockQuerier) GetProductForOrder(ctx context.Context, id int64) (db.Product, error) {
	row, err := q.Querier.GetProductForOrder(ctx, id)
	if err == nil {
		err = q.pause(ctx)
	}
	return row, err
}

type stockHistoryLockTx struct {
	biz.TxManager
	barrier *orderUserLockBarrier
}

func (tx stockHistoryLockTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		pgTx := pgTxFromContext(ctx)
		q := stockHistoryLockQuerier{orderUserLockQuerier{querierFromContext(ctx, nil), pgTx, tx.barrier}}
		return fn(WithQuerier(ctx, q, pgTx))
	})
}

func TestStockAdjustmentAndProductViewDoNotDeadlockIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	barrier := &orderUserLockBarrier{loaded: make(chan struct{}), release: make(chan struct{}), deletePID: make(chan uint32, 1)}
	var once sync.Once
	release := func() { once.Do(func() { close(barrier.release) }) }
	var workers sync.WaitGroup
	defer func() { release(); cancel(); workers.Wait() }()
	products := NewProductRepo(f.data, stockHistoryLockTx{f.tx, barrier}, log.DefaultLogger)
	history := NewBrowsingHistoryRepo(f.data, orderUserLockTx{f.tx, barrier}, biz.CommunityPolicy{})
	adjusted, viewed := make(chan error, 1), make(chan error, 1)
	workers.Go(func() {
		_, err := products.AdjustStock(ctx, biz.StockAdjustmentInput{ProductID: f.productID, ActorID: f.userID, Delta: 1, Reason: "restock", IdempotencyKey: "history-overlap"})
		adjusted <- err
	})
	select {
	case <-barrier.loaded:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	workers.Go(func() { _, err := history.Record(ctx, f.userID, f.productID); viewed <- err })
	var pid uint32
	select {
	case pid = <-barrier.deletePID:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Eventually(t, func() bool {
		var blocked bool
		err := f.pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(pid)).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond)
	// Previously the view held the user row while waiting for this product;
	// releasing the writer then deadlocked at the stock-adjustment actor FK.
	release()
	for _, done := range []chan error{adjusted, viewed} {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var stock, count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 101, stock)
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM product_browsing_history WHERE user_id=$1 AND product_id=$2`, f.userID, f.productID).Scan(&count))
	require.Equal(t, 1, count)
}
