package data

import (
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

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

func TestGenerateOrderNo32_FormatAndLength(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 3})
	require.NoError(t, err)

	no := g.GenerateOrderNo32("AB")
	assert.Len(t, no, 32)
	assert.Equal(t, "AB", no[:2])

	wantStamp := time.Now().Format("20060102150405")
	assert.Equal(t, wantStamp, no[2:16])
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), no[16:])
}

func TestGenerateOrderNo32_NormalizesPrefix(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 3})
	require.NoError(t, err)

	tooLong := g.GenerateOrderNo32("ABCDE")
	assert.Len(t, tooLong, 32)
	assert.Equal(t, "AB", tooLong[:2])

	tooShort := g.GenerateOrderNo32("A")
	assert.Len(t, tooShort, 32)
	assert.Equal(t, "A ", tooShort[:2])
}

func TestGenerateOrderNo32_Unique(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 4})
	require.NoError(t, err)

	const n = 200
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		no := g.GenerateOrderNo32("OD")
		assert.Len(t, no, 32)
		_, dup := seen[no]
		assert.Falsef(t, dup, "duplicate order no on iteration %d: %s", i, no)
		seen[no] = struct{}{}
	}
}

func TestGenerateOrderNo64_FormatAndLength(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 3})
	require.NoError(t, err)

	const userID = 12345678
	no := g.GenerateOrderNo64("ABCD", userID)
	assert.Len(t, no, 64)
	assert.Equal(t, "ABCD", no[:4])

	wantStamp := time.Now().Format("20060102150405")
	assert.Equal(t, wantStamp, no[4:18])
	assert.Equal(t, "12345678", no[18:26])
	assert.Regexp(t, regexp.MustCompile(`^\d{19}$`), no[26:45])
	assert.Regexp(t, regexp.MustCompile(`^[A-Z0-9]{19}$`), no[45:])
}

func TestGenerateOrderNo64_NormalizesPrefix(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 3})
	require.NoError(t, err)

	tooLong := g.GenerateOrderNo64("ABCDEFG", 1)
	assert.Len(t, tooLong, 64)
	assert.Equal(t, "ABCD", tooLong[:4])

	tooShort := g.GenerateOrderNo64("AB", 1)
	assert.Len(t, tooShort, 64)
	assert.Equal(t, "AB  ", tooShort[:4])
}

func TestGenerateOrderNo64_PadsUserID(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 3})
	require.NoError(t, err)

	no := g.GenerateOrderNo64("PAY", 42)
	assert.Len(t, no, 64)
	assert.Equal(t, "00000042", no[18:26])
}

func TestGenerateOrderNo64_Unique(t *testing.T) {
	g, err := NewSnowflakeIDGenerator(&conf.Snowflake{NodeId: 4})
	require.NoError(t, err)

	const (
		n      = 200
		userID = 7
	)
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		no := g.GenerateOrderNo64("PAY", userID)
		assert.Len(t, no, 64)
		_, dup := seen[no]
		assert.Falsef(t, dup, "duplicate order no on iteration %d: %s", i, no)
		seen[no] = struct{}{}
	}
}
