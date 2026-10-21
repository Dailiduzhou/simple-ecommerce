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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Pause a real transaction after it owns the rows needed by an address write.
type addressMutationQuerier struct {
	db.Querier
	pauseOn         string
	loaded, release chan struct{}
	lockPID         chan uint32
	pid             uint32
	zeroRows        bool
}

func (q addressMutationQuerier) pause(ctx context.Context, operation string) error {
	if q.pauseOn != operation {
		return nil
	}
	close(q.loaded)
	select {
	case <-q.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q addressMutationQuerier) LockUserForAddress(ctx context.Context, id int64) (int64, error) {
	if q.lockPID != nil {
		q.lockPID <- q.pid
	}
	return q.Querier.LockUserForAddress(ctx, id)
}

func (q addressMutationQuerier) GetShippingAddressForUpdate(ctx context.Context, args db.GetShippingAddressForUpdateParams) (db.ShippingAddress, error) {
	row, err := q.Querier.GetShippingAddressForUpdate(ctx, args)
	if err == nil {
		err = q.pause(ctx, "set")
	}
	return row, err
}

func (q addressMutationQuerier) DeleteShippingAddress(ctx context.Context, args db.DeleteShippingAddressParams) error {
	if err := q.Querier.DeleteShippingAddress(ctx, args); err != nil {
		return err
	}
	return q.pause(ctx, "delete")
}

func (q addressMutationQuerier) CreateShippingAddress(ctx context.Context, args db.CreateShippingAddressParams) (db.ShippingAddress, error) {
	row, err := q.Querier.CreateShippingAddress(ctx, args)
	if err == nil {
		err = q.pause(ctx, "create")
	}
	return row, err
}

func (q addressMutationQuerier) SetDefaultShippingAddress(ctx context.Context, args db.SetDefaultShippingAddressParams) (int64, error) {
	if q.zeroRows {
		return 0, nil
	}
	return q.Querier.SetDefaultShippingAddress(ctx, args)
}

type addressMutationTx struct {
	biz.TxManager
	hooks addressMutationQuerier
}

func (tx addressMutationTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		q := tx.hooks
		q.Querier = querierFromContext(ctx, nil)
		q.pid = pgTxFromContext(ctx).Conn().PgConn().PID()
		return fn(WithQuerier(ctx, q, nil))
	})
}

func createDefaultAddress(ctx context.Context, repo *ShippingAddressRepo, userID int64) (*biz.ShippingAddress, error) {
	return repo.CreateShippingAddress(ctx, userID, "receiver", "hash", "cipher", "p", "c", "d", "detail", "", true)
}

func TestAddressDefaultMutationsSerializeIntegration(t *testing.T) {
	for _, pair := range [][2]string{
		{"set", "delete"}, {"delete", "set"}, {"set", "set"},
		{"set", "create"}, {"create", "set"}, {"create", "delete"}, {"delete", "create"},
	} {
		t.Run(pair[0]+"_then_"+pair[1], func(t *testing.T) {
			f := newCorrectnessFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			loaded, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			resume := func() { once.Do(func() { close(release) }) }
			var workers sync.WaitGroup
			defer func() {
				resume()
				cancel()
				workers.Wait()
			}()
			baseRepo := NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger)
			oldDefault, err := createDefaultAddress(ctx, baseRepo, f.userID)
			require.NoError(t, err)

			firstTx := addressMutationTx{f.tx, addressMutationQuerier{pauseOn: pair[0], loaded: loaded, release: release}}
			lockPID := make(chan uint32, 1)
			secondTx := addressMutationTx{f.tx, addressMutationQuerier{lockPID: lockPID}}
			firstRepo := NewShippingAddressRepo(f.data, firstTx, log.DefaultLogger)
			secondRepo := NewShippingAddressRepo(f.data, secondTx, log.DefaultLogger)
			type result struct {
				createdID int64
				err       error
			}
			write := func(repo *ShippingAddressRepo, operation string, target int64) result {
				switch operation {
				case "set":
					return result{err: repo.SetDefaultShippingAddress(ctx, target, f.userID)}
				case "delete":
					return result{err: repo.DeleteShippingAddress(ctx, target, f.userID)}
				default:
					address, err := createDefaultAddress(ctx, repo, f.userID)
					if err != nil {
						return result{err: err}
					}
					return result{createdID: address.ID}
				}
			}
			firstDone := make(chan result, 1)
			workers.Go(func() { firstDone <- write(firstRepo, pair[0], f.addressID) })
			select {
			case <-loaded:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			secondTarget := f.addressID
			if pair == [2]string{"set", "set"} {
				secondTarget = oldDefault.ID
			}
			secondDone := make(chan result, 1)
			workers.Go(func() { secondDone <- write(secondRepo, pair[1], secondTarget) })
			var pid uint32
			select {
			case pid = <-lockPID:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.Eventually(t, func() bool {
				var blocked bool
				err := f.pool.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1)) > 0", int32(pid)).Scan(&blocked)
				return err == nil && blocked
			}, 3*time.Second, 10*time.Millisecond, "every default-affecting write must wait on the same user lock")
			resume()
			first, second := <-firstDone, <-secondDone
			require.NoError(t, first.err)
			expected := oldDefault.ID
			if pair == [2]string{"delete", "set"} {
				require.ErrorIs(t, second.err, biz.ErrShippingAddressNotFound)
			} else {
				require.NoError(t, second.err)
				switch pair[1] {
				case "create":
					expected = second.createdID
				case "set":
					expected = secondTarget
				case "delete":
					expected = first.createdID // zero when deleting the just-set default is intentional
				}
			}
			defaults, err := f.pool.Query(ctx, "SELECT id FROM shipping_addresses WHERE user_id=$1 AND is_default", f.userID)
			require.NoError(t, err)
			var ids []int64
			for defaults.Next() {
				var id int64
				require.NoError(t, defaults.Scan(&id))
				ids = append(ids, id)
			}
			require.NoError(t, defaults.Err())
			defaults.Close()
			if expected == 0 {
				require.Empty(t, ids)
			} else {
				require.Equal(t, []int64{expected}, ids)
			}
		})
	}
}

func TestSetDefaultAddressLocksTargetIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	loaded, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	defer func() { resume(); cancel(); workers.Wait() }()
	tx := addressMutationTx{f.tx, addressMutationQuerier{pauseOn: "set", loaded: loaded, release: release}}
	repo := NewShippingAddressRepo(f.data, tx, log.DefaultLogger)
	done := make(chan error, 1)
	workers.Go(func() { done <- repo.SetDefaultShippingAddress(ctx, f.addressID, f.userID) })
	select {
	case <-loaded:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	deleter, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer deleter.Rollback(context.Background())
	_, err = deleter.Exec(ctx, "SET LOCAL lock_timeout='100ms'")
	require.NoError(t, err)
	_, err = deleter.Exec(ctx, "DELETE FROM shipping_addresses WHERE id=$1", f.addressID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "55P03", pgErr.Code, "even a delete bypassing the user-lock protocol must wait on the target row")
	require.NoError(t, deleter.Rollback(ctx))
	resume()
	require.NoError(t, <-done)
}

func TestSetDefaultAddressZeroRowsRollsBackClearIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	repo := NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger)
	oldDefault, err := createDefaultAddress(f.ctx, repo, f.userID)
	require.NoError(t, err)
	tx := addressMutationTx{f.tx, addressMutationQuerier{zeroRows: true}}
	repo = NewShippingAddressRepo(f.data, tx, log.DefaultLogger)
	require.ErrorIs(t, repo.SetDefaultShippingAddress(f.ctx, f.addressID, f.userID), biz.ErrShippingAddressNotFound)
	stored, err := f.data.DB(f.ctx).GetDefaultShippingAddress(f.ctx, f.userID)
	require.NoError(t, err)
	require.Equal(t, oldDefault.ID, stored.ID, "the clear must be rolled back when no target was updated")
}
