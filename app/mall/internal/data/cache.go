package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

const negativeCacheTTL = 30 * time.Second

func cacheTTL() time.Duration {
	return 10*time.Minute + time.Duration(rand.Intn(10))*time.Minute
}

// Transaction snapshots must neither consume nor populate shared caches, nor
// share a singleflight result with another transaction/request.
func inTransaction(ctx context.Context) bool {
	return ctx.Value(ctxTxKey{}) != nil || ctx.Value(txStateKey{}) != nil
}

// An empty key explicitly disables caching (e.g. when generation GET fails).
func readJSONCache[T any](ctx context.Context, d *Data, key string) (T, error) {
	var value T
	if key == "" || inTransaction(ctx) || d.rdb == nil {
		return value, redis.Nil
	}
	encoded, err := d.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(encoded, &value)
	return value, err
}

func cacheFailure(ctx context.Context, logger *log.Helper, operation, key string, err error) {
	// Never attach IDs, phone hashes or pagination values to metric labels.
	entity, _, _ := strings.Cut(key, ":")
	switch entity {
	case "user", "shipping_addr", "product", "category", "event", "order", "payment":
	default:
		entity = "unknown"
	}
	observability.CacheFailure(ctx, operation, entity)
	logger.WithContext(ctx).Errorw("msg", "cache operation failed", "operation", operation, "key", key, "error", err)
}

// Serialize before registering the callback: callers may mutate the returned
// object before the surrounding transaction commits.
func writeJSONCache(ctx context.Context, d *Data, logger *log.Helper, key string, value any, ttl time.Duration) {
	if key == "" || d.rdb == nil {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		cacheFailure(ctx, logger, "marshal", key, err)
		return
	}
	if bytes.Equal(encoded, []byte("null")) {
		ttl = negativeCacheTTL
	}
	afterCommit(ctx, func() {
		// A committed mutation still needs invalidation/publication if the
		// HTTP client has disconnected. Keep that best-effort work bounded.
		cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := d.rdb.Set(cacheCtx, key, encoded, ttl).Err(); err != nil {
			cacheFailure(cacheCtx, logger, "set", key, err)
		}
	})
}

func deleteJSONCache(ctx context.Context, d *Data, logger *log.Helper, keys ...string) {
	afterCommit(ctx, func() {
		if d.rdb == nil || len(keys) == 0 {
			return
		}
		cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := d.rdb.Unlink(cacheCtx, keys...).Err(); err != nil {
			cacheFailure(cacheCtx, logger, "unlink", keys[0], err)
		}
	})
}

// cacheAside caches successful loads, including typed nil (not-found) values.
// Database errors are never cached. The full Redis key is also the singleflight
// key, so owners, query parameters and generations all remain isolated.
func cacheAside[T any](ctx context.Context, d *Data, logger *log.Helper, key string, read func(context.Context, string) (T, error), write func(context.Context, string, T), load func() (T, error)) (T, error) {
	if key == "" || inTransaction(ctx) || d.rdb == nil {
		return load()
	}
	get := func() (T, error) {
		value, err := read(ctx, key)
		if err != nil && !errors.Is(err, redis.Nil) {
			cacheFailure(ctx, logger, "get", key, err)
		}
		return value, err
	}
	if value, err := get(); err == nil {
		return value, nil
	}
	result := d.sg.DoChan("sf:"+key, func() (any, error) {
		if value, err := get(); err == nil {
			return value, nil
		}
		value, err := load()
		if err == nil {
			write(ctx, key, value)
		}
		return value, err
	})
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			var zero T
			return zero, result.Err
		}
		return result.Val.(T), nil
	}
}

func generationCacheKey(generation int64, key string) string {
	if generation < 0 {
		return ""
	}
	return key
}

// generatedEntityCacheKey scopes a single-entity cache key to the current value
// of its generation key. Writers advance the generation after commit, so a load
// that started before a concurrent delete/update can no longer publish a stale
// value under a reachable key (the old generation expires with its TTL). A
// failed generation GET disables caching: it must never fall back to an
// unversioned key that old writers could still target.
func generatedEntityCacheKey(ctx context.Context, d *Data, logger *log.Helper, generationKey, baseKey string) string {
	generation := readCacheGeneration(ctx, d.rdb, logger, generationKey)
	if generation < 0 {
		return ""
	}
	return redisKey(baseKey, "g", generation)
}
