package biz

import "context"

type TxManager interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	// InTxSnapshot runs fn in a read-only REPEATABLE READ transaction: every
	// statement inside observes the same database snapshot. Paginated reads
	// that issue more than one statement use it so a page cannot mix two
	// points in time.
	InTxSnapshot(ctx context.Context, fn func(ctx context.Context) error) error
}
