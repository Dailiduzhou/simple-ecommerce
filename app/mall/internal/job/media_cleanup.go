package job

import (
	"context"
	"errors"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/riverqueue/river"
)

type MediaSweepWorker struct {
	river.WorkerDefaults[biz.MediaSweepArgs]
	uc *biz.MediaUsecase
}

func NewMediaSweepWorker(u *biz.MediaUsecase) *MediaSweepWorker { return &MediaSweepWorker{uc: u} }
func (w *MediaSweepWorker) Work(ctx context.Context, j *river.Job[biz.MediaSweepArgs]) error {
	_, e := w.uc.Sweep(ctx)
	result := "success"
	if e != nil {
		result = "error"
	}
	observability.CommunityEvent(ctx, "media_sweep", result)
	return e
}

type MediaDeleteWorker struct {
	river.WorkerDefaults[biz.MediaDeleteArgs]
	uc *biz.MediaUsecase
}

func NewMediaDeleteWorker(u *biz.MediaUsecase) *MediaDeleteWorker { return &MediaDeleteWorker{uc: u} }
func (w *MediaDeleteWorker) Work(ctx context.Context, j *river.Job[biz.MediaDeleteArgs]) error {
	if j.Args.MediaID <= 0 {
		observability.CommunityEvent(ctx, "media_delete", "invalid_args")
		return river.JobCancel(errors.New("invalid media ID"))
	}
	e := w.uc.Remove(ctx, j.Args.MediaID)
	if errors.Is(e, biz.ErrMediaCleanupNotDue) {
		return river.JobSnooze(time.Minute)
	}
	result := "success"
	if e != nil {
		result = "error"
	}
	observability.CommunityEvent(ctx, "media_delete", result)
	return e
}
