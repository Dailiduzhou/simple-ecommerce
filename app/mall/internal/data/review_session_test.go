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

func TestSessionOwnershipRevocationAndFixedLifetime(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	r := NewAuthRepo(rdb, log.DefaultLogger)
	ctx := context.Background()
	require.NoError(t, r.CreateSession(ctx, "s", 1, time.Minute))
	require.Error(t, r.CreateSession(ctx, "s", 2, time.Minute))
	active, err := r.SessionActive(ctx, "s", 2)
	require.NoError(t, err)
	require.False(t, active)
	ok, err := r.ConsumeSessionRefresh(ctx, "s", 2, "jti", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = r.RevokeSession(ctx, "s", 2)
	require.NoError(t, err)
	require.False(t, ok)
	mr.FastForward(10 * time.Second)
	ok, err = r.ConsumeSessionRefresh(ctx, "s", 1, "jti", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 50*time.Second, mr.TTL(sessionKey("s")))
	require.Equal(t, 50*time.Second, mr.TTL("auth:session:{s}:used:jti"))
	for range 2 {
		ok, err = r.RevokeSession(ctx, "s", 1)
		require.NoError(t, err)
		require.True(t, ok)
	}
	require.Equal(t, 50*time.Second, mr.TTL(sessionKey("s")))
	active, err = r.SessionActive(ctx, "s", 1)
	require.NoError(t, err)
	require.False(t, active)
	ok, err = r.ConsumeSessionRefresh(ctx, "s", 1, "descendant", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	mr.FastForward(time.Minute)
	ok, err = r.RevokeSession(ctx, "s", 1)
	require.NoError(t, err)
	require.True(t, ok, "expired revocation is already satisfied")
}

func TestSessionStoreLossFailsClosed(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	r := NewAuthRepo(rdb, log.DefaultLogger)
	ctx := context.Background()
	require.NoError(t, r.CreateSession(ctx, "s", 1, time.Minute))
	mr.FlushAll()
	active, err := r.SessionActive(ctx, "s", 1)
	require.NoError(t, err)
	require.False(t, active)
	ok, err := r.ConsumeSessionRefresh(ctx, "s", 1, "jti", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	mr.SetError("unavailable")
	_, err = r.SessionActive(ctx, "s", 1)
	require.Error(t, err)
	_, err = r.RevokeSession(ctx, "s", 1)
	require.Error(t, err)
}
