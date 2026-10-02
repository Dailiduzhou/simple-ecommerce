//go:build integration

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/stretchr/testify/require"
)

func TestDefaultAddressLockAllowsConcurrentCheckoutIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	_, err := f.pool.Exec(ctx, `UPDATE shipping_addresses SET receiver_phone_encrypt=$2 WHERE id=$1`, f.addressID, shippingCipher(t, "13800138000"))
	require.NoError(t, err)

	// Pause checkout after its address FOR SHARE, before the order INSERT's
	// foreign-key check takes KEY SHARE on the user.
	barrier := shippingSnapshotTx{TxManager: f.tx, loaded: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(barrier.release) }) }
	defer release()
	checkoutDone := make(chan error, 1)
	go func() {
		_, err := shippingOrderUsecase(f, barrier).CreateOrder(ctx, shippingCheckout(f))
		checkoutDone <- err
	}()
	select {
	case <-barrier.loaded:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// Default-address switching locks the user first, then waits for the
	// address held by checkout. This must not block checkout's user FK check.
	switchTx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer switchTx.Rollback(context.Background())
	q := db.New(switchTx)
	_, err = q.LockUserForAddress(ctx, f.userID)
	require.NoError(t, err)
	pid := switchTx.Conn().PgConn().PID()
	switchDone := make(chan error, 1)
	go func() {
		switchDone <- q.ClearDefaultShippingAddress(ctx, f.userID)
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		err := f.pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(pid)).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "default-address update must be waiting for checkout's address lock")

	release()
	select {
	case err := <-checkoutDone:
		require.NoError(t, err, "checkout must complete while the default-address transaction holds its user lock")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-switchDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, q.SetDefaultShippingAddress(ctx, db.SetDefaultShippingAddressParams{ID: f.addressID, UserID: f.userID}))
	require.NoError(t, switchTx.Commit(ctx))
}
