package data

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	communityv1 "github.com/Dailiduzhou/simple-ecommerce/api/community/v1"
	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestWriteRateLimitIsAtomicAndExpires(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer r.Close()
	l, e := NewWriteLimiter(r, &conf.Community{PostsPerMinute: 3})
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
	l, e := NewWriteLimiter(r, nil)
	require.NoError(t, e)
	require.NoError(t, l.Allow(ctx, 1, "ip", communityv1.OperationCommunityGetPost))
	require.Error(t, l.Allow(ctx, 1, "ip", mediav1.OperationMediaCreateImageUpload))
	l, e = NewWriteLimiter(r, &conf.Community{RateLimitFailOpen: true})
	require.NoError(t, e)
	require.NoError(t, l.Allow(ctx, 1, "ip", mediav1.OperationMediaCreateImageUpload))
}
