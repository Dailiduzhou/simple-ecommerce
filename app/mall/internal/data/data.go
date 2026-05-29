package data

import (
	"context"
	"fmt"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"golang.org/x/sync/singleflight"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewRedisClient)

// Data .
type Data struct {
	pool        *pgxpool.Pool
	riverclient *river.Client[pgx.Tx]
	rdb         *redis.Client
	q           *db.Queries
	sg          *singleflight.Group
}

// NewData .
func NewData(c *conf.Data, pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx], rdb *redis.Client) (*Data, func(), error) {
	ctx := context.Background()

	cleanup := func() {
		riverClient.Stop(ctx)
		rdb.Close()
		pool.Close()

		log.Info("closing the data resources")
	}
	return &Data{
		pool:        pool,
		riverclient: riverClient,
		rdb:         rdb,
		q:           db.New(pool),
		sg:          &singleflight.Group{},
	}, cleanup, nil
}

func NewRedisClient(c *conf.Data) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     c.Redis.Addr,
		Password: "",
		DB:       0,
	})

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return rdb, nil
}
