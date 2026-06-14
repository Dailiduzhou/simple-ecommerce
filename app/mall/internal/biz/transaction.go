package biz

import "context"

type TxManager interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}
