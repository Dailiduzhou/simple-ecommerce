//go:build integration

package data

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
)

func TestMigrationsUseDedicatedAccountAndHonourDisableIntegration(t *testing.T) {
	dsn := os.Getenv(integrationPostgresEnv)
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL migration verification", integrationPostgresEnv)
	}
	require.Equal(t, "true", os.Getenv("ECOMMERCE_INTEGRATION_EXCLUSIVE"), "migration tests require disposable infrastructure")
	adminConfig, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(adminConfig.Database), "integration")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	require.NoError(t, err)
	defer admin.Close(context.Background())

	suffix := fmt.Sprint(time.Now().UnixNano())
	migrator, runtime := "it_migrator_"+suffix, "it_runtime_"+suffix
	databases := []string{"ecommerce_integration_migrations_" + suffix, "ecommerce_integration_disabled_" + suffix}
	quote := func(name string) string { return pgx.Identifier{name}.Sanitize() }
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, database := range databases {
			_, err := admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+quote(database)+" WITH (FORCE)")
			require.NoError(t, err)
		}
		for _, role := range []string{runtime, migrator} {
			_, err := admin.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+quote(role))
			require.NoError(t, err)
		}
	}()
	for _, role := range []string{migrator, runtime} {
		_, err := admin.Exec(ctx, "CREATE ROLE "+quote(role)+" LOGIN PASSWORD 'integration-only' NOSUPERUSER NOCREATEDB NOCREATEROLE")
		require.NoError(t, err)
	}
	for _, database := range databases {
		_, err := admin.Exec(ctx, "CREATE DATABASE "+quote(database)+" OWNER "+quote(migrator))
		require.NoError(t, err)
	}
	baseURL, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, baseURL.Scheme)
	accountDSN := func(database, user string) string {
		endpoint := *baseURL
		endpoint.Path = "/" + database
		endpoint.User = url.UserPassword(user, "integration-only")
		return endpoint.String()
	}
	// The runtime account starts without any schema creation privileges.
	databaseConfig := adminConfig.Copy()
	databaseConfig.Database = databases[0]
	grantConn, err := pgx.ConnectConfig(ctx, databaseConfig)
	require.NoError(t, err)
	defer grantConn.Close(context.Background())
	_, err = grantConn.Exec(ctx, "REVOKE CREATE ON SCHEMA public FROM PUBLIC")
	require.NoError(t, err)
	config := &conf.Data{Database: &conf.Data_Database{
		Source: accountDSN(databases[0], runtime), MigrationSource: accountDSN(databases[0], migrator),
	}}
	pool, cleanup, err := NewPgxPool(config)
	require.NoError(t, err, "both schemas must migrate using the dedicated account")
	defer cleanup()
	_, err = grantConn.Exec(ctx, "GRANT USAGE ON SCHEMA public TO "+quote(runtime))
	require.NoError(t, err)
	_, err = grantConn.Exec(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO "+quote(runtime))
	require.NoError(t, err)
	_, err = grantConn.Exec(ctx, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO "+quote(runtime))
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	var mallTables, riverVersions int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&mallTables))
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM river_migration").Scan(&riverVersions))
	require.Positive(t, riverVersions)
	_, err = pool.Exec(ctx, "CREATE TABLE runtime_ddl_must_fail(id bigint)")
	var permissionErr *pgconn.PgError
	require.ErrorAs(t, err, &permissionErr)
	require.Equal(t, "42501", permissionErr.Code)

	workers := river.NewWorkers()
	river.AddWorker(workers, integrationCheckPayWorker{})
	client, err := NewRiverClient(pool, workers, nil, NewPaymentRiverErrorHandler(pool, nil, log.DefaultLogger))
	require.NoError(t, err, "constructing a runtime River client must not perform DDL")
	events, unsubscribe := client.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()
	require.NoError(t, client.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Stop(stopCtx))
	}()
	inserted, err := client.Insert(ctx, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, &river.InsertOpts{Queue: "payments"})
	require.NoError(t, err)
	select {
	case event := <-events:
		require.Equal(t, inserted.Job.ID, event.Job.ID)
	case <-ctx.Done():
		t.Fatal("DML-only River worker did not complete its job: ", ctx.Err())
	}

	// An empty database with migrations disabled must stay empty, even when
	// the migration DSN is unusable and the River client is constructed.
	disabled := &conf.Data{Database: &conf.Data_Database{
		Source: accountDSN(databases[1], runtime), MigrationSource: "postgres://unreachable.invalid/integration", DisableMigrations: true,
	}}
	disabledPool, closeDisabled, err := NewPgxPool(disabled)
	require.NoError(t, err)
	defer closeDisabled()
	require.NoError(t, disabledPool.Ping(ctx))
	_, err = NewRiverClient(disabledPool, workers, nil, NewPaymentRiverErrorHandler(disabledPool, nil, log.DefaultLogger))
	require.NoError(t, err)
	var schemasAbsent bool
	require.NoError(t, disabledPool.QueryRow(ctx, "SELECT to_regclass('public.users') IS NULL AND to_regclass('public.river_migration') IS NULL").Scan(&schemasAbsent))
	require.True(t, schemasAbsent, "neither mall nor River migrations may run when disabled")
}
