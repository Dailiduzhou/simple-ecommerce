package job

import (
	"context"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/riverqueue/river"
)

type CacheInvalidationWorker struct {
	river.WorkerDefaults[biz.CacheInvalidationArgs]
	repo biz.CacheInvalidationRepo
}

func NewCacheInvalidationWorker(repo biz.CacheInvalidationRepo) *CacheInvalidationWorker {
	return &CacheInvalidationWorker{repo: repo}
}

func (w *CacheInvalidationWorker) Work(ctx context.Context, _ *river.Job[biz.CacheInvalidationArgs]) error {
	count, err := w.repo.FlushCacheInvalidations(ctx, 1000)
	if err != nil {
		observability.CacheRecovery(ctx, "failed", 1)
		return err
	}
	observability.CacheRecovery(ctx, "recovered", int64(count))
	return nil
}
