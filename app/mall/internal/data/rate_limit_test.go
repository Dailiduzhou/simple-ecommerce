package data

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestWriteRateLimitIsAtomicAndExpires(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	l, e := NewWriteLimiter(r, &conf.Community{PostsPerMinute: 3}, nil)
	require.NoError(t, e)
	ctx := context.Background()
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := l.Allow(ctx, 1, "ip", communityv1.OperationCommunityCreatePost)
			if e == nil {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 3, passed.Load())
	m.FastForward(time.Minute)
	require.NoError(t, l.Allow(ctx, 1, "ip", communityv1.OperationCommunityCreatePost))
	// Same-IP quota is shared even across multiple user accounts.
	for i := int64(2); i <= 15; i++ {
		require.NoError(t, l.Allow(ctx, i, "ip", communityv1.OperationCommunityCreatePost))
	}
	require.True(t, mediav1.IsRateLimited(l.Allow(ctx, 100, "ip", communityv1.OperationCommunityCreatePost)))
	require.NoError(t, l.Allow(ctx, 1, "ip", communityv1.OperationCommunityGetPost))
	require.NoError(t, l.Allow(ctx, 1, "other-ip", communityv1.OperationCommunityCreatePost))
}

func TestLimiterFailurePolicyDoesNotAffectReads(t *testing.T) {
	r := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 5 * time.Millisecond, MaxRetries: -1})
	defer r.Close()
	ctx := context.Background()
	l, e := NewWriteLimiter(r, nil, nil)
	require.NoError(t, e)
	require.NoError(t, l.Allow(ctx, 1, "ip", communityv1.OperationCommunityGetPost))
	require.Error(t, l.Allow(ctx, 1, "ip", mediav1.OperationMediaCreateImageUpload))
	l, e = NewWriteLimiter(r, &conf.Community{RateLimitFailOpen: true}, nil)
	require.NoError(t, e)
	require.NoError(t, l.Allow(ctx, 1, "ip", mediav1.OperationMediaCreateImageUpload))
}

// Login, register and refresh reach the limiter without claims; anonymous
// requests are throttled on the IP dimension alone.
func TestWriteRateLimitAuthAnonymousIPDimension(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	l, e := NewWriteLimiter(r, nil, &conf.Auth{AuthRequestsPerMinute: 3})
	require.NoError(t, e)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		require.NoError(t, l.Allow(ctx, 0, "1.2.3.4", userv1.OperationUserLogin))
	}
	require.True(t, mediav1.IsRateLimited(l.Allow(ctx, 0, "1.2.3.4", userv1.OperationUserLogin)))
	// Same category: all three auth operations share the window.
	require.True(t, mediav1.IsRateLimited(l.Allow(ctx, 0, "1.2.3.4", userv1.OperationUserRegister)))
	require.True(t, mediav1.IsRateLimited(l.Allow(ctx, 0, "1.2.3.4", userv1.OperationUserRefreshToken)))
	// A different IP has its own window.
	require.NoError(t, l.Allow(ctx, 0, "5.6.7.8", userv1.OperationUserLogin))
	// The window is fixed and expires without extension.
	m.FastForward(time.Minute)
	require.NoError(t, l.Allow(ctx, 0, "1.2.3.4", userv1.OperationUserLogin))
}

// A signed-in caller on an auth operation keeps the dual user+IP windows.
func TestWriteRateLimitAuthAuthenticatedKeepsUserDimension(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	l, e := NewWriteLimiter(r, nil, &conf.Auth{AuthRequestsPerMinute: 2})
	require.NoError(t, e)
	ctx := context.Background()
	require.NoError(t, l.Allow(ctx, 7, "1.2.3.4", userv1.OperationUserLogin))
	require.NoError(t, l.Allow(ctx, 7, "1.2.3.4", userv1.OperationUserLogin))
	require.True(t, mediav1.IsRateLimited(l.Allow(ctx, 7, "1.2.3.4", userv1.OperationUserLogin)))
}

// Anonymous callers on any other category are still rejected, not throttled.
func TestWriteRateLimitNonAuthAnonymousStillUnauthorized(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	l, e := NewWriteLimiter(r, nil, nil)
	require.NoError(t, e)
	require.Error(t, l.Allow(context.Background(), 0, "1.2.3.4", communityv1.OperationCommunityCreatePost))
}

// Auth throttling must fail closed on Redis outages and reject absurd config
// values at construction, mirroring the community buckets.
func TestWriteRateLimitAuthFailClosedAndConfigBounds(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	l, e := NewWriteLimiter(r, &conf.Community{}, &conf.Auth{AuthRequestsPerMinute: 5})
	require.NoError(t, e)
	r.Close()
	require.Error(t, l.Allow(context.Background(), 0, "1.2.3.4", userv1.OperationUserLogin))
	_, e = NewWriteLimiter(r, nil, &conf.Auth{AuthRequestsPerMinute: -1})
	require.Error(t, e)
	_, e = NewWriteLimiter(r, nil, &conf.Auth{LoginMaxAttempts: -1})
	require.Error(t, e)
}
