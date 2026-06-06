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

	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		require.False(t, entry.IsDir())
		names[entry.Name()] = true
	}

	assert.True(t, names["000001_init_schema.up.sql"])
	assert.True(t, names["000001_init_schema.down.sql"])
	assert.True(t, names["000002_add_category_sort_order.up.sql"])
	assert.True(t, names["000002_add_category_sort_order.down.sql"])

	initSchema, err := fs.ReadFile(dbmigrations.FS, "migrations/000001_init_schema.up.sql")
	require.NoError(t, err)
	assert.Contains(t, string(initSchema), "CREATE TABLE users")

	sortOrderMigration, err := fs.ReadFile(dbmigrations.FS, "migrations/000002_add_category_sort_order.up.sql")
	require.NoError(t, err)
	assert.Contains(t, string(sortOrderMigration), "ALTER TABLE categories")
}
