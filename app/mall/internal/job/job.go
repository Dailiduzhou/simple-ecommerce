package job

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

var ProviderSet = wire.NewSet(NewRiverServer, NewWorkers, NewCheckPayWorker)

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

func NewWorkers(checkPayWorker *CheckPayWorker, logger log.Logger) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, checkPayWorker)
	log.NewHelper(logger).Infof("registered river worker kind=%s", biz.CheckPayJobKind)
	return workers
}
