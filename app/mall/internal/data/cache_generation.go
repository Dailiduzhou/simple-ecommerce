package data

import (
	"context"
	stderrors "errors"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

// cacheGeneration returns zero for a missing generation key. Writers advance
// it with INCR after the surrounding database transaction commits, so old
// paginated values become unreachable without a Redis SCAN.
func cacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) int64 {
	generation, err := rdb.Get(ctx, key).Int64()
	if err == nil {
		return generation
	}
	if !stderrors.Is(err, redis.Nil) {
		observability.CacheFailure(ctx, "get_generation", key)
		logger.WithContext(ctx).Errorw("msg", "read cache generation failed", "operation", "get_generation", "key", key, "error", err)
	}
	return 0
}

func bumpCacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) {
	afterCommit(ctx, func() {
		if err := rdb.Incr(ctx, key).Err(); err != nil {
			observability.CacheFailure(ctx, "incr_generation", key)
			logger.WithContext(ctx).Errorw("msg", "advance cache generation failed", "operation", "incr_generation", "key", key, "error", err)
		}
	})
}
