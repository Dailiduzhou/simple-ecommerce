package data

import (
	"context"
	"io/fs"
	"testing"

	dbmigrations "github.com/Dailiduzhou/simple-ecommerce/app/mall/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/golang/mock/gomock"
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
		"000002_products_category_nullable.up.sql",
		"000002_products_category_nullable.down.sql",
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

// 改写 init SQL 只对新库生效：已经记录为 migrated 的部署不会重新执行它。可空分类列
// 与配套约束/触发器必须同时以前向迁移的形式存在，否则线上库停留在旧形态。
func TestForwardMigrationCoversRewrittenInitSchema(t *testing.T) {
	up, err := fs.ReadFile(dbmigrations.FS, "migrations/000002_products_category_nullable.up.sql")
	require.NoError(t, err)
	down, err := fs.ReadFile(dbmigrations.FS, "migrations/000002_products_category_nullable.down.sql")
	require.NoError(t, err)

	assert.Contains(t, string(up), "ALTER TABLE products ALTER COLUMN category_id DROP NOT NULL")
	assert.Contains(t, string(up), "ADD CONSTRAINT products_category_check")
	assert.Contains(t, string(up), "CREATE OR REPLACE FUNCTION enqueue_projection_invalidation()")

	assert.Contains(t, string(down), "ALTER TABLE products ALTER COLUMN category_id SET NOT NULL")
	assert.Contains(t, string(down), "DROP CONSTRAINT IF EXISTS products_category_check")

	// 迁移必须与 init SQL 描述同一套 schema，任何一侧单独改动都会被这里发现。
	initSchema, err := fs.ReadFile(dbmigrations.FS, "migrations/000001_init_schema.up.sql")
	require.NoError(t, err)
	for _, fragment := range []string{
		"CHECK (deleted_at IS NOT NULL OR category_id IS NOT NULL)",
		"'product:category:' || COALESCE(current_row.category_id, 0) || ':gen'",
		"IF TG_OP = 'UPDATE' AND OLD.category_id IS DISTINCT FROM NEW.category_id THEN",
	} {
		assert.Contains(t, string(initSchema), fragment)
		assert.Contains(t, string(up), fragment)
	}
}

// A nested InTx must fail loudly instead of quietly opening a second
// transaction whose writes commit independently of the outer one.
func TestInTxRejectsNestedTransactions(t *testing.T) {
	tx := &transaction{}
	outer := context.WithValue(context.Background(), txStateKey{}, &txState{})
	err := tx.InTx(outer, func(context.Context) error {
		t.Fatal("a nested call must not run the body")
		return nil
	})
	require.ErrorContains(t, err, "transaction already active")

	// A context that only carries the transaction querier is equally nested.
	injected := WithQuerier(context.Background(), mockdb.NewMockQuerier(gomock.NewController(t)), nil)
	require.ErrorContains(t, tx.InTx(injected, func(context.Context) error { return nil }), "transaction already active")
}
