package job

import (
	"context"

	"github.com/google/wire"
	"github.com/jackc/pgx"
	"github.com/riverqueue/river"
)

var ProviderSet = wire.NewSet(NewRiverServer)

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
