package data

import (
	"context"
	"errors"

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

func NewTransaction(pool *pgxpool.Pool, logger log.Logger) biz.TxManager {
	return &transaction{pool: pool, log: log.NewHelper(logger)}
}

var _ biz.TxManager = (*transaction)(nil)

func (t *transaction) InTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				t.log.WithContext(ctx).Errorf("rollback failed: %v (original error: %v)", rbErr, err)
			}
			return
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			err = cerr
		}
	}()

	ctx = context.WithValue(ctx, ctxTxKey{}, db.New(tx))
	ctx = context.WithValue(ctx, ctxRawPgTxKey{}, tx)
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
