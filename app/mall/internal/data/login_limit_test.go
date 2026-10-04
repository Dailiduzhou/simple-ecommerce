package data

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAccountLoginQuotaRefillsWithoutExtendingDenials(t *testing.T) {
	mr := miniredis.RunT(t)
	now := time.Unix(1800000000, 0)
	mr.SetTime(now)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	repo := NewAuthRepo(rdb, log.DefaultLogger)
	ctx := context.Background()
	reserve := func(hash string) time.Duration {
		wait, err := repo.ReserveLoginAttempt(ctx, hash, 5, 30*time.Second)
		require.NoError(t, err)
		return wait
	}
	for range 5 {
		require.Zero(t, reserve("hash-a"))
	}
	require.Equal(t, 30*time.Second, reserve("hash-a"))
	require.Zero(t, reserve("hash-b"), "another account keeps its own budget")
	key := redisKey("auth", "login", "budget", "hash-a")
	advance := func(d time.Duration) { now = now.Add(d); mr.SetTime(now); mr.FastForward(d) }
	advance(29 * time.Second)
	before, err := mr.Get(key)
	require.NoError(t, err)
	ttl := mr.TTL(key)
	for range 100 {
		require.Equal(t, time.Second, reserve("hash-a"))
	}
	after, err := mr.Get(key)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, ttl, mr.TTL(key), "denied traffic must never postpone replenishment")
	advance(time.Second)
	require.Zero(t, reserve("hash-a"), "one slot refills automatically")
	require.Equal(t, 30*time.Second, reserve("hash-a"))
	advance(150 * time.Second)
	require.False(t, mr.Exists(key))
	for range 5 {
		require.Zero(t, reserve("hash-a"))
	}
}

func TestAccountLoginQuotaRejectsInvalidPolicyAndStoreFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	repo := NewAuthRepo(rdb, log.DefaultLogger)
	for _, tc := range []struct {
		hash     string
		burst    int64
		interval time.Duration
	}{
		{"", 5, time.Second}, {"hash", 0, time.Second}, {"hash", 101, time.Second},
		{"hash", 5, time.Millisecond}, {"hash", 5, 61 * time.Second},
	} {
		_, err := repo.ReserveLoginAttempt(context.Background(), tc.hash, tc.burst, tc.interval)
		require.Error(t, err)
	}
	mr.SetError("unavailable")
	_, err := repo.ReserveLoginAttempt(context.Background(), "hash", 5, time.Second)
	require.Error(t, err)
}
