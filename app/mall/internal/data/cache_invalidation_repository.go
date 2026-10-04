package data

import (
	"context"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
)

type CacheInvalidationRepo struct {
	data *Data
	tx   biz.TxManager
	log  *log.Helper
}

func NewCacheInvalidationRepo(data *Data, tx biz.TxManager, logger log.Logger) *CacheInvalidationRepo {
	return &CacheInvalidationRepo{data: data, tx: tx, log: log.NewHelper(logger)}
}

var _ biz.CacheInvalidationRepo = (*CacheInvalidationRepo)(nil)

func (r *CacheInvalidationRepo) FlushCacheInvalidations(ctx context.Context, limit int32) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("cache invalidation batch must be between 1 and 1000")
	}
	if r.data.rdb == nil {
		return 0, fmt.Errorf("cache invalidation requires Redis")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	count := 0
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := r.data.DB(ctx)
		rows, err := q.LockCacheInvalidations(ctx, limit)
		if err != nil || len(rows) == 0 {
			return err
		}
		batch := newCacheInvalidation(ctx, r.data.rdb, r.log)
		ids := make([]int64, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
			batch.add(row.GenerationKeys, nil)
		}
		// Keep database row locks across the single bounded Redis round trip.
		// Partial pipeline failure or a failed commit leaves every row retryable.
		if err := batch.flush(); err != nil {
			return err
		}
		if err := q.DeleteCacheInvalidations(ctx, ids); err != nil {
			return err
		}
		count = len(rows)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
