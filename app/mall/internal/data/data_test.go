package data

import (
	"context"
	"io/fs"
	"testing"

	dbmigrations "github.com/Dailiduzhou/simple-ecommerce/app/mall/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without a password Redis is world-readable: anyone who can reach the port can
// drop the jwt blacklist and limiter keys, so the client must send whatever the
// configuration provides and select the configured database.
func TestNewRedisClientHonoursPasswordAndDB(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.RequireAuth("s3cret")

	_, err := NewRedisClient(&conf.Data{Redis: &conf.Data_Redis{Addr: mr.Addr()}})
	require.Error(t, err, "an unauthenticated client must not start against an authenticated Redis")

	client, err := NewRedisClient(&conf.Data{Redis: &conf.Data_Redis{Addr: mr.Addr(), Password: "s3cret", Db: 3}})
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.Set(context.Background(), "blacklist:jti", "1", 0).Err())
	require.True(t, mr.DB(3).Exists("blacklist:jti"))
	require.False(t, mr.DB(0).Exists("blacklist:jti"))
}

func TestRunMigrationsRequiresDatabaseSource(t *testing.T) {
	tests := []struct {
		name string
		data *conf.Data
	}{
		{name: "nil data"},
		{name: "nil database", data: &conf.Data{}},
		{name: "empty source", data: &conf.Data{Database: &conf.Data_Database{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RunMigrations(tt.data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "database source is required")
		})
	}
}

// Production must be able to hand migrations to CI/a DBA: the disable switch
// skips them entirely, even when a DSN is present.
func TestRunMigrationsHonoursTheDisableSwitch(t *testing.T) {
	c := &conf.Data{Database: &conf.Data_Database{
		Source:            "postgres://unreachable.invalid/ecommerce",
		MigrationSource:   "postgres://migration.invalid/ecommerce",
		DisableMigrations: true,
	}}
	require.NoError(t, RunMigrations(c), "a disabled migrator must not contact the database")
}

func TestEmbeddedMigrationsAvailable(t *testing.T) {
	entries, err := fs.ReadDir(dbmigrations.FS, "migrations")
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		require.False(t, entry.IsDir())
		names = append(names, entry.Name())
	}

	assert.ElementsMatch(t, []string{
		"000001_init_schema.up.sql",
		"000001_init_schema.down.sql",
	}, names)

	initSchema, err := fs.ReadFile(dbmigrations.FS, "migrations/000001_init_schema.up.sql")
	require.NoError(t, err)
	assert.Contains(t, string(initSchema), "CREATE TABLE users")
	assert.Contains(t, string(initSchema), "sort_order INTEGER")
	assert.Contains(t, string(initSchema), "CREATE TABLE payment_notifications")
	assert.Contains(t, string(initSchema), "CREATE TABLE payment_reconciliation_failures")
	assert.Contains(t, string(initSchema), "payment_id BIGINT")
	assert.Contains(t, string(initSchema), "last_error TEXT NOT NULL DEFAULT ''")
	assert.Contains(t, string(initSchema), "CREATE UNIQUE INDEX idx_order_refunds_payment_id")
}
