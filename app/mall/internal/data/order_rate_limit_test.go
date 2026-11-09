package data

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	orderv1 "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCheckoutQuotaIsAtomicUserOnlyAndExpires(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = r.Close() })
	limiter, err := NewWriteLimiter(r, nil, nil)
	require.NoError(t, err)
	ctx := context.Background()
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if limiter.Allow(ctx, 1, fmt.Sprintf("ip-%d", i), orderv1.OperationOrderCreateOrder) == nil {
				passed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 10, passed.Load(), "switching IPs does not bypass the user quota")
	// More users than a dual-dimension IP budget permits must remain independent.
	for uid := int64(2); uid <= 12; uid++ {
		for i := 0; i < 10; i++ {
			require.NoError(t, limiter.Allow(ctx, uid, "shared-nat", orderv1.OperationOrderCreateOrder))
		}
	}
	for _, key := range m.Keys() {
		require.Contains(t, key, "order:limit:user:", "checkout must not consume an IP bucket")
	}
	m.FastForward(50 * time.Second)
	require.EqualValues(t, 429, errors.FromError(limiter.Allow(ctx, 1, "new-ip", orderv1.OperationOrderCreateOrder)).Code)
	m.FastForward(10 * time.Second)
	require.NoError(t, limiter.Allow(ctx, 1, "new-ip", orderv1.OperationOrderCreateOrder), "denials must not extend the window")
	require.NoError(t, limiter.Allow(ctx, 1, "ip", orderv1.OperationOrderGetOrder))
	require.NoError(t, limiter.Allow(ctx, 1, "ip", orderv1.OperationOrderCancelOrder))
	require.EqualValues(t, 401, errors.FromError(limiter.Allow(ctx, 0, "ip", orderv1.OperationOrderCreateOrder)).Code)
}

func TestCheckoutQuotaFailsClosedDespiteCommunityPolicy(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	limiter, err := NewWriteLimiter(r, &conf.Community{RateLimitFailOpen: true}, nil)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	err = limiter.Allow(context.Background(), 1, "ip", orderv1.OperationOrderCreateOrder)
	require.EqualValues(t, 503, errors.FromError(err).Code)
}
