package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/job"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/logger"
	appmetrics "github.com/Dailiduzhou/simple-ecommerce/pkg/metrics"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/trace"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/config"
	"github.com/go-kratos/kratos/v2/config/env"
	"github.com/go-kratos/kratos/v2/config/file"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	"github.com/go-kratos/kratos/v2/transport/http"

	_ "go.uber.org/automaxprocs"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	// Name is the name of the compiled software.
	Name = "simple-ecommerce"
	// Version is the version of the compiled software.
	Version string
	// flagconf is the config flag.
	flagconf string

	id, _ = os.Hostname()
)

func init() {
	flag.StringVar(&flagconf, "conf", "../../configs", "config path, eg: -conf config.yaml")
}

func newApp(logger log.Logger, gs *grpc.Server, hs *http.Server, rs *job.RiverServer) *kratos.App {
	return kratos.New(
		kratos.ID(id),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Metadata(map[string]string{}),
		kratos.Logger(logger),
		kratos.Server(
			gs,
			hs,
			rs,
		),
	)
}

func main() {
	flag.Parse()

	logger := logger.NewJSONLogger()
	log.SetLogger(logger)

	cleanupTrace := trace.InitTracer(Name)
	defer cleanupTrace()
	cleanupMetrics := appmetrics.InitMeter(Name)
	defer cleanupMetrics()

	c := config.New(
		config.WithSource(
			env.NewSource(""),
			file.NewSource(flagconf),
		),
	)
	defer c.Close()

	if err := c.Load(); err != nil {
		panic(err)
	}

	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		panic(err)
	}
	if err := validateBootstrap(&bc); err != nil {
		panic(err)
	}

	app, cleanup, err := wireApp(bc.Server, bc.Data, bc.Auth, bc.Snowflake, bc.Payment, logger)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	// start and wait for stop signal
	if err := app.Run(); err != nil {
		panic(err)
	}
}

func validateBootstrap(bc *conf.Bootstrap) error {
	if bc == nil || bc.Auth == nil {
		return fmt.Errorf("auth configuration is required")
	}
	if len(bc.Auth.AccessTokenSecret) < 32 {
		return fmt.Errorf("AUTH_ACCESS_TOKEN_SECRET must contain at least 32 bytes")
	}
	if len(bc.Auth.RefreshTokenSecret) < 32 {
		return fmt.Errorf("AUTH_REFRESH_TOKEN_SECRET must contain at least 32 bytes")
	}
	if bc.Auth.AccessTokenSecret == bc.Auth.RefreshTokenSecret {
		return fmt.Errorf("access and refresh token secrets must differ")
	}
	if len(bc.Auth.PhoneSecret) < 32 {
		return fmt.Errorf("AUTH_PHONE_SECRET must contain at least 32 bytes")
	}
	return nil
}
