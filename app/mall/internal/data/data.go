package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"os"

	dbmigrations "github.com/Dailiduzhou/simple-ecommerce/app/mall/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"golang.org/x/sync/singleflight"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(
	NewPgxPool, NewRiverClient, NewData, NewRedisClient, NewAuthRepo, NewUserRepo, NewShippingAddressRepo, NewProductRepo, NewCategoryRepo, NewEventRepo, NewOrderRepo, NewWechatPaymentAdapter, NewAlipayPaymentAdapter, NewPaymentAdapters, NewPaymentRepo, NewPaymentMQRepo, NewTransaction, NewAlipayClient, NewSnowflakeIDGenerator,
	wire.Bind(new(biz.AuthRepo), new(*AuthRepo)),
	wire.Bind(new(biz.UserRepo), new(*UserRepo)),
	wire.Bind(new(biz.ShippingAddressRepo), new(*ShippingAddressRepo)),
	wire.Bind(new(biz.ProductRepo), new(*ProductRepo)),
	wire.Bind(new(biz.CategoryRepo), new(*CategoryRepo)),
	wire.Bind(new(biz.EventRepo), new(*EventRepo)),
	wire.Bind(new(biz.OrderRepo), new(*OrderRepo)),
	wire.Bind(new(biz.PaymentRepo), new(*PaymentRepo)),
	wire.Bind(new(biz.PaymentMQRepo), new(*PaymentMQRepo)),
	wire.Bind(new(biz.IDGenerator), new(*snowflakeGenerator)),
)

type (
	ctxTxKey      struct{}
	ctxRawPgTxKey struct{}
)

// Data .
type Data struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	q    db.Querier
	sg   *singleflight.Group
}

// NewData .
func NewData(c *conf.Data, pool *pgxpool.Pool, rdb *redis.Client) (*Data, func(), error) {
	cleanup := func() {
		rdb.Close()
		pool.Close()

		log.Info("closing the data resources")
	}
	return &Data{
		pool: pool,
		rdb:  rdb,
		q:    db.New(pool),
		sg:   &singleflight.Group{},
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

func NewPgxPool(c *conf.Data) (*pgxpool.Pool, func(), error) {
	if err := RunMigrations(c); err != nil {
		return nil, nil, err
	}

	pool, err := pgxpool.New(context.Background(), c.Database.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("create pgx pool: %w", err)
	}
	cleanup := func() { pool.Close() }
	return pool, cleanup, nil
}

func NewRiverClient(pool *pgxpool.Pool, workers *river.Workers) (*river.Client[pgx.Tx], error) {
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return nil, fmt.Errorf("create river migrator: %w", err)
	}
	if _, err := migrator.Migrate(context.Background(), rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("run river migrations: %w", err)
	}

	client, err := river.NewClient(driver, &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 10},
			"payments":         {MaxWorkers: 10},
		},
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("create river client: %w", err)
	}
	return client, nil
}

func RunMigrations(c *conf.Data) error {
	if c == nil || c.Database == nil || c.Database.Source == "" {
		return fmt.Errorf("database source is required for migrations")
	}

	sourceDriver, err := iofs.New(dbmigrations.FS, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", sourceDriver, c.Database.Source)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := m.Close()
		if sourceErr != nil {
			log.Errorf("close migration source: %v", sourceErr)
		}
		if databaseErr != nil {
			log.Errorf("close migration database: %v", databaseErr)
		}
	}()

	if err := m.Up(); err != nil {
		if stderrors.Is(err, migrate.ErrNoChange) {
			log.Info("database migrations are up to date")
			return nil
		}
		return fmt.Errorf("run database migrations: %w", err)
	}

	log.Info("database migrations applied")
	return nil
}

func NewAlipayClient(c *conf.Payment) (*alipayv3.ClientV3, error) {
	aliConf := c.GetAlipay()

	client, err := alipayv3.NewClientV3(aliConf.AppId, aliConf.PrivateKey, aliConf.IsProduction)
	if err != nil {
		return nil, err
	}

	// 读出三张证书的 PEM 字节流交给 v3 客户端 SetCert。v3 客户端没有
	// SetCertSnByPath 之类按路径设置的 API,统一在启动期读一次比每次签名
	// 时再读要快,也避免证书丢失/权限问题延迟到首次请求才暴露。
	appCert, err := os.ReadFile(aliConf.AppCertPath)
	if err != nil {
		return nil, fmt.Errorf("read alipay app cert %q: %w", aliConf.AppCertPath, err)
	}
	rootCert, err := os.ReadFile(aliConf.AlipayRootCertPath)
	if err != nil {
		return nil, fmt.Errorf("read alipay root cert %q: %w", aliConf.AlipayRootCertPath, err)
	}
	publicCert, err := os.ReadFile(aliConf.AlipayPublicCertPath)
	if err != nil {
		return nil, fmt.Errorf("read alipay public cert %q: %w", aliConf.AlipayPublicCertPath, err)
	}
	if err := client.SetCert(appCert, rootCert, publicCert); err != nil {
		return nil, fmt.Errorf("set alipay certs: %w", err)
	}

	return client, nil
}

func (d *Data) DB(ctx context.Context) db.Querier {
	return querierFromContext(ctx, d.q)
}

func (d *Data) GetPgTx(ctx context.Context) pgx.Tx {
	return pgTxFromContext(ctx)
}

// pgTxFromContext extracts the raw pgx.Tx from the context. It is used by
// data-layer repos that need to participate in an ongoing transaction.
func pgTxFromContext(ctx context.Context) pgx.Tx {
	if tx, ok := ctx.Value(ctxRawPgTxKey{}).(pgx.Tx); ok {
		return tx
	}
	return nil
}

func querierFromContext(ctx context.Context, fallback db.Querier) db.Querier {
	if q, ok := ctx.Value(ctxTxKey{}).(db.Querier); ok {
		return q
	}
	return fallback
}
