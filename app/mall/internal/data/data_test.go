package data

import (
	"io/fs"
	"testing"

	dbmigrations "github.com/Dailiduzhou/simple-ecommerce/app/mall/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
