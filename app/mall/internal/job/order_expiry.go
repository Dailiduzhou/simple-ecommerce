package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/riverqueue/river"
)

type ExpireOrderWorker struct {
	river.WorkerDefaults[biz.ExpireOrderArgs]
	repo biz.OrderExpiryRepo
}

func NewExpireOrderWorker(repo biz.OrderExpiryRepo) *ExpireOrderWorker {
	return &ExpireOrderWorker{repo: repo}
}

func (w *ExpireOrderWorker) Work(ctx context.Context, job *river.Job[biz.ExpireOrderArgs]) error {
	if job.Args.OrderID <= 0 || w.repo == nil {
		return river.JobCancel(fmt.Errorf("expire_order requires order_id and repository"))
	}
	err := w.repo.ExpireOrder(ctx, job.Args.OrderID)
	if errors.Is(err, biz.ErrOrderNotExpired) {
		return river.JobSnooze(time.Minute)
	}
	if errors.Is(err, biz.ErrPaymentReconciliationRequired) {
		return river.JobSnooze(5 * time.Minute)
	}
	if errors.Is(err, biz.ErrOrderNotFound) {
		return river.JobCancel(err)
	}
	return err
}
