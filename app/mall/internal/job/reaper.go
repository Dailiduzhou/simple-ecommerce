package job

import (
	"context"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
)

const (
	// reapInterval is how often the backstop sweeps run.
	reapInterval = time.Minute
	// reapOrderGrace keeps the order reaper away from orders whose expiry job
	// may still be healthy: only windows that ended this long ago are re-enqueued.
	reapOrderGrace = 5 * time.Minute
	// refundReconcileGrace keeps the refund sweeper away from refunds that are
	// merely in flight; only records pending this long are retried.
	refundReconcileGrace = 10 * time.Minute
	reapBatchLimit       = 100
)

// ReapExpiredOrdersWorker is the backstop for expire_order jobs that were lost
// or discarded after exhausting retries: an order left in pending_payment
// forever would also keep its stock reserved. The sweep itself is idempotent
// because re-enqueueing relies on River's ByArgs uniqueness.
type ReapExpiredOrdersWorker struct {
	river.WorkerDefaults[biz.ReapExpiredOrdersArgs]
	repo biz.OrderExpiryRepo
	log  *log.Helper
}

func NewReapExpiredOrdersWorker(repo biz.OrderExpiryRepo, logger log.Logger) *ReapExpiredOrdersWorker {
	return &ReapExpiredOrdersWorker{repo: repo, log: log.NewHelper(logger)}
}

func (w *ReapExpiredOrdersWorker) Work(ctx context.Context, job *river.Job[biz.ReapExpiredOrdersArgs]) error {
	if w.repo == nil {
		return river.JobCancel(fmt.Errorf("order reaper requires a repository"))
	}
	requeued, err := w.repo.ReapOverdueOrders(ctx, reapOrderGrace, reapBatchLimit)
	if err != nil {
		return err
	}
	if len(requeued) > 0 {
		w.log.WithContext(ctx).Warnw("msg", "order reaper re-enqueued overdue orders", "event", "order_expiry_reaped", "count", len(requeued))
	}
	return nil
}

// ReconcileRefundsWorker retries refunds stuck in pending — typically the
// gateway accepted the refund but the process died before the local settlement
// committed. Retries reuse the original OutRefundNo, which gateways treat
// idempotently.
type ReconcileRefundsWorker struct {
	river.WorkerDefaults[biz.ReconcileRefundsArgs]
	paymentUc biz.PaymentUsecase
	log       *log.Helper
}

func NewReconcileRefundsWorker(paymentUc biz.PaymentUsecase, logger log.Logger) *ReconcileRefundsWorker {
	return &ReconcileRefundsWorker{paymentUc: paymentUc, log: log.NewHelper(logger)}
}

func (w *ReconcileRefundsWorker) Work(ctx context.Context, job *river.Job[biz.ReconcileRefundsArgs]) error {
	if w.paymentUc == nil {
		return river.JobCancel(fmt.Errorf("refund reconciler requires a payment usecase"))
	}
	settled, err := w.paymentUc.ReconcilePendingRefunds(ctx, refundReconcileGrace, reapBatchLimit)
	if err != nil {
		return err
	}
	if settled > 0 {
		w.log.WithContext(ctx).Infow("msg", "refund reconciler settled pending refunds", "event", "refund_reconciled", "count", settled)
	}
	return nil
}

// NewPeriodicJobs builds the backstop schedules. River's periodic scheduler
// inserts the job once per trigger across the whole cluster, so multiple app
// instances do not multiply the sweep.
func NewPeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(reapInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return biz.ReapExpiredOrdersArgs{}, &river.InsertOpts{Queue: "orders", MaxAttempts: 1}
			},
			&river.PeriodicJobOpts{},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(reapInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return biz.ReconcileRefundsArgs{}, &river.InsertOpts{Queue: "payments", MaxAttempts: 1}
			},
			&river.PeriodicJobOpts{},
		),
	}
}
