package data

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
)

type transaction struct {
	data *Data
}

func NewTransaction(data *Data) biz.Transaction {
	return &transaction{data: data}
}

var _ biz.Transaction = (*transaction)(nil)

func (t *transaction) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := t.data.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qTx := db.New(tx)
	ctx = context.WithValue(ctx, ctxTxKey{}, qTx)
	ctx = context.WithValue(ctx, ctxRawPgTxKey{}, tx)

	if err := fn(ctx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
