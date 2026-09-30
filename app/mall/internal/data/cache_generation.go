package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

// cacheGeneration returns zero for a missing generation key. Writers advance
// it with INCR after the surrounding database transaction commits, so old
// paginated values become unreachable without a Redis SCAN. A negative result
// disables caching: a failed GET must never resurrect generation-zero entries.
func cacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) int64 {
	if rdb == nil {
		return -1
	}
	generation, err := rdb.Get(ctx, key).Int64()
	if err == nil && generation >= 0 {
		return generation
	}
	if err == nil {
		err = fmt.Errorf("negative cache generation: %d", generation)
	}
	if !stderrors.Is(err, redis.Nil) {
		cacheFailure(ctx, logger, "get_generation", key, err)
		return -1
	}
	return 0
}

func bumpCacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) {
	scheduleCacheInvalidation(ctx, rdb, logger, []string{key}, nil)
}

// readCacheGeneration avoids even Redis metadata reads inside a transaction.
func readCacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) int64 {
	if inTransaction(ctx) {
		return -1
	}
	return cacheGeneration(ctx, rdb, logger, key)
}
