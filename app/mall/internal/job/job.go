package job

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

var ProviderSet = wire.NewSet(NewRiverServer, NewWorkers, NewCheckPayWorker, NewExpireOrderWorker, NewClosePayWorker, NewReapExpiredOrdersWorker, NewReconcileRefundsWorker, NewPeriodicJobs)

type RiverServer struct {
	client *river.Client[pgx.Tx]
}

func NewRiverServer(riverClient *river.Client[pgx.Tx]) *RiverServer {
	return &RiverServer{client: riverClient}
}

func (s *RiverServer) Start(ctx context.Context) error {
	return s.client.Start(ctx)
}

func (s *RiverServer) Stop(ctx context.Context) error {
	return s.client.Stop(ctx)
}

func NewWorkers(checkPayWorker *CheckPayWorker, expireOrderWorker *ExpireOrderWorker, closePayWorker *ClosePayWorker, reapExpiredOrdersWorker *ReapExpiredOrdersWorker, reconcileRefundsWorker *ReconcileRefundsWorker, logger log.Logger) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, checkPayWorker)
	river.AddWorker(workers, expireOrderWorker)
	river.AddWorker(workers, closePayWorker)
	river.AddWorker(workers, reapExpiredOrdersWorker)
	river.AddWorker(workers, reconcileRefundsWorker)
	log.NewHelper(logger).Infof("registered river worker kinds=%s,%s,%s,%s,%s", biz.CheckPayJobKind, biz.ExpireOrderJobKind, biz.ClosePayJobKind, biz.ReapExpiredOrdersJobKind, biz.ReconcileRefundsJobKind)
	return workers
}
