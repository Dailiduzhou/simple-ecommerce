package job

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/riverqueue/river"
)

type BrowsingHistoryCleanupWorker struct {
	river.WorkerDefaults[biz.HistoryCleanupArgs]
	uc *biz.BrowsingHistoryUsecase
}

func NewBrowsingHistoryCleanupWorker(u *biz.BrowsingHistoryUsecase) *BrowsingHistoryCleanupWorker {
	return &BrowsingHistoryCleanupWorker{uc: u}
}

func (w *BrowsingHistoryCleanupWorker) Work(ctx context.Context, j *river.Job[biz.HistoryCleanupArgs]) error {
	_, e := w.uc.Cleanup(ctx)
	result := "success"
	if e != nil {
		result = "error"
	}
	observability.CommunityEvent(ctx, "history_cleanup", result)
	return e
}
