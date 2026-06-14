package data

import (
	"strconv"
	"sync"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSnowflakeIDGenerator_ConfPreferred(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 7})
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.Implements(t, (*biz.IDGenerator)(nil), g)
}

func TestNewSnowflakeIDGenerator_FallsBackToEnv(t *testing.T) {
	t.Setenv(EnvSnowflakeNodeID, "42")

	g, err := NewSnowflakeIDGenerator(nil)
	require.NoError(t, err)
	require.NotNil(t, g)
}

func TestNewSnowflakeIDGenerator_RejectsInvalidEnv(t *testing.T) {
	t.Setenv(EnvSnowflakeNodeID, "not-a-number")

	_, err := NewSnowflakeIDGenerator(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), EnvSnowflakeNodeID)
}

func TestNewSnowflakeIDGenerator_RequiresNodeID(t *testing.T) {
	t.Setenv(EnvSnowflakeNodeID, "")

	_, err := NewSnowflakeIDGenerator(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snowflake node_id is required")
}

func TestNewSnowflakeIDGenerator_RejectsOutOfRange(t *testing.T) {
	t.Setenv(EnvSnowflakeNodeID, strconv.FormatInt(snowflakeMaxNode()+1, 10))

	_, err := NewSnowflakeIDGenerator(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestSnowflakeGenerator_GeneratesUniqueIDs(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 5})
	require.NoError(t, err)

	const n = 200
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := g.GenerateString()
		assert.NotEmpty(t, id)
		_, dup := seen[id]
		assert.Falsef(t, dup, "duplicate id generated on iteration %d: %s", i, id)
		seen[id] = struct{}{}
	}
}

func TestSnowflakeGenerator_Concurrent(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 9})
	require.NoError(t, err)

	const (
		workers       = 8
		perWorker     = 250
		expectedTotal = workers * perWorker
	)

	var wg sync.WaitGroup
	ids := make(chan string, expectedTotal)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				ids <- g.GenerateString()
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]struct{}, expectedTotal)
	for id := range ids {
		_, dup := seen[id]
		assert.Falsef(t, dup, "duplicate id under concurrency: %s", id)
		seen[id] = struct{}{}
	}
	assert.Equal(t, expectedTotal, len(seen))
}
