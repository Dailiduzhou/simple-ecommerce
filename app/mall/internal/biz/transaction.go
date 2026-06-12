package biz

import "context"

type Transaction interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}
