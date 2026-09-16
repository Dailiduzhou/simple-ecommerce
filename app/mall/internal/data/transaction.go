package data

import (
	"context"
	"errors"
	"fmt"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type transaction struct {
	pool *pgxpool.Pool
	log  *log.Helper
}

type (
	txStateKey struct{}
	txState    struct {
		afterCommit []func()
	}
)

func NewTransaction(pool *pgxpool.Pool, logger log.Logger) biz.TxManager {
	return &transaction{pool: pool, log: log.NewHelper(logger)}
}

var _ biz.TxManager = (*transaction)(nil)

func (t *transaction) InTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	// A nested call would silently open a second, independent transaction whose
	// writes commit or roll back on their own. Callers inside a transaction must
	// reuse the context they were handed instead.
	if inTransaction(ctx) {
		return fmt.Errorf("transaction already active: reuse the context passed to InTx instead of nesting")
	}
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return err
	}
	state := &txState{}

	defer func() {
		// Rollback and commit must survive a client disconnect: the outcome of an
		// already-started transaction is decided by PostgreSQL, not by whether the
		// caller is still connected.
		detached := context.WithoutCancel(ctx)
		if p := recover(); p != nil {
			_ = tx.Rollback(detached)
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(detached); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				t.log.WithContext(ctx).Errorf("rollback failed: %v (original error: %v)", rbErr, err)
			}
			return
		}
		if cerr := tx.Commit(detached); cerr != nil {
			err = cerr
			return
		}
		for _, cb := range state.afterCommit {
			cb()
		}
	}()

	ctx = context.WithValue(ctx, ctxTxKey{}, db.New(tx))
	ctx = context.WithValue(ctx, ctxRawPgTxKey{}, tx)
	ctx = context.WithValue(ctx, txStateKey{}, state)
	return fn(ctx)
}

// WithQuerier injects a Querier and a raw pgx.Tx into ctx. It is used by the
// data layer when a tx has been started; tests can use it to inject a mock
// Querier without spinning up a real database.
func WithQuerier(ctx context.Context, q db.Querier, tx pgx.Tx) context.Context {
	ctx = context.WithValue(ctx, ctxTxKey{}, q)
	if tx != nil {
		ctx = context.WithValue(ctx, ctxRawPgTxKey{}, tx)
	}
	return ctx
}

func afterCommit(ctx context.Context, fn func()) {
	if state, ok := ctx.Value(txStateKey{}).(*txState); ok && state != nil {
		state.afterCommit = append(state.afterCommit, fn)
		return
	}
	fn()
}
