package data

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRiverMigrationDSNFiltersOnlyMigrateOptions(t *testing.T) {
	config, err := riverMigrationPoolConfig("postgres://migrator:secret@db.example:5433/shop?sslmode=require&search_path=mall&application_name=migrate&x-statement-timeout=60000&x-migrations-table=mall_versions")
	require.NoError(t, err)
	c := config.ConnConfig
	require.Equal(t, "migrator", c.User)
	require.Equal(t, "secret", c.Password)
	require.Equal(t, "db.example", c.Host)
	require.EqualValues(t, 5433, c.Port)
	require.Equal(t, "shop", c.Database)
	require.NotNil(t, c.TLSConfig)
	require.Equal(t, map[string]string{"search_path": "mall", "application_name": "migrate"}, c.RuntimeParams)
}
