package data

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

const (
	negativeCacheTTL = 30 * time.Second

	// cacheWriteTimeout bounds a single cache SET. Reads use the smaller of this
	// and their own parent deadline.
	cacheWriteTimeout = 2 * time.Second
)

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
	observability.CacheFailure(ctx, operation, cacheEntity(key))
	logger.WithContext(ctx).Errorw("msg", "cache operation failed", "operation", operation, "key", key, "error", err)
}

// cacheEntity maps a full Redis key to a bounded metric/span label.
func cacheEntity(key string) string {
	entity, _, _ := strings.Cut(key, ":")
	switch entity {
	case "user", "shipping_addr", "product", "category", "event", "order", "payment":
		return entity
	default:
		return "unknown"
	}
}

// encodeCacheValue serializes a cache value and switches typed nil (not-found)
// values to the negative-cache TTL. Encoding happens before any shared result is
// published, so callers may still mutate the object afterwards.
func encodeCacheValue(value any, ttl time.Duration) ([]byte, time.Duration, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, ttl, err
	}
	if string(encoded) == "null" {
		ttl = negativeCacheTTL
	}
	return encoded, ttl, nil
}

// fillJSONCache publishes read-through results synchronously within the shared
// work budget. Transaction snapshots must never be published, even after commit.
func fillJSONCache(ctx context.Context, d *Data, logger *log.Helper, key string, value any, ttl time.Duration) {
	if key == "" || d.rdb == nil || inTransaction(ctx) || ctx.Err() != nil {
		return
	}
	encoded, ttl, err := encodeCacheValue(value, ttl)
	if err != nil {
		cacheFailure(ctx, logger, "marshal", key, err)
		return
	}
	// Respect parent cancellation/deadline; never detach read-through fills.
	cacheCtx, cancel := context.WithTimeout(ctx, cacheWriteTimeout)
	defer cancel()
	if err := d.rdb.Set(cacheCtx, key, encoded, ttl).Err(); err != nil {
		cacheFailure(cacheCtx, logger, "set", key, err)
	}
}

func deleteJSONCache(ctx context.Context, d *Data, logger *log.Helper, keys ...string) {
	scheduleCacheInvalidation(ctx, d.rdb, logger, nil, keys)
}

// cacheAside caches successful loads, including typed nil (not-found) values.
// Database errors are never cached. The full Redis key is also the singleflight
// key, so owners, query parameters and generations all remain isolated.
// Returned pointers and slices may be shared by singleflight callers. Treat
// results as read-only and copy them before any request-specific mutation.
//
// Shared work reuses the leader's context values after its request has ended,
// so load must derive the result from the arguments it captures alone: never
// from request-scoped values (tenant, read-your-writes, feature flags) that
// could differ between callers sharing one flight. Values reached through the
// context must also stay valid and must not be caller-owned mutable objects.
func cacheAside[T any](ctx context.Context, d *Data, logger *log.Helper, key string, read func(context.Context, string) (T, error), write func(context.Context, string, T), load func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if key == "" || inTransaction(ctx) || d.rdb == nil {
		return load(ctx)
	}
	get := func(readCtx context.Context) (T, error) {
		value, err := read(readCtx, key)
		if err != nil && !errors.Is(err, redis.Nil) {
			cacheFailure(readCtx, logger, "get", key, err)
		}
		return value, err
	}
	// The initial lookup and waiting belong to this request.
	if value, err := get(ctx); err == nil {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return value, nil
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	entity := cacheEntity(key)
	budget := d.sharedCacheTimeout()
	resultCh := d.sg.DoChan("sf:"+key, func() (any, error) {
		// Only the executing callback owns cancellation of shared work. Keep
		// stable context values, but not the leader's deadline/cancellation.
		// All waiters may leave; work can still fill the cache within its budget.
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		defer cancel()
		// The request span may already have ended by the time this work finishes.
		workCtx, endSpan := observability.StartSharedCacheWork(workCtx, entity, budget)
		defer endSpan()
		observability.CacheSharedWorkStarted(workCtx, entity)
		result := "loaded"
		defer func() { observability.CacheSharedWorkFinished(workCtx, entity, result) }()
		// Report an exhausted budget once per flight, not once per waiter.
		timedOut := func(err error) error {
			result = "timeout"
			cacheFailure(workCtx, logger, "work_timeout", key, err)
			return err
		}

		if value, err := get(workCtx); err == nil {
			result = "cache_hit"
			return value, nil
		}
		if err := workCtx.Err(); err != nil {
			return zero, timedOut(err)
		}
		value, err := load(workCtx)
		if err != nil {
			// A database error caused by the budget expiring is a timeout, not a
			// database failure; keep the two separate for operators.
			if workErr := workCtx.Err(); workErr != nil {
				return zero, timedOut(workErr)
			}
			result = "error"
			return zero, err
		}
		if err := workCtx.Err(); err != nil {
			return zero, timedOut(err)
		}
		// A failed/timed-out fill must not hide a successful database result.
		write(workCtx, key, value)
		return value, nil
	})
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case result := <-resultCh:
		// Both branches may be ready: prefer observed request cancellation.
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if result.Err != nil {
			return zero, sharedWorkError(ctx, result.Err)
		}
		return result.Val.(T), nil
	}
}

// sharedWorkError labels a shared-budget timeout. It is not this caller's own
// deadline: the request is still healthy and could reasonably retry, so mixing
// the two into the same opaque error hides retryable slow reads behind what
// looks like an internal failure.
func sharedWorkError(ctx context.Context, err error) error {
	if ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// HTTP status stays 500: the public OpenAPI contract does not define a
	// gateway-timeout response for these endpoints. The reason code makes the
	// case distinguishable in logs, metrics and response bodies.
	return kerrors.InternalServer("CACHE_WORK_TIMEOUT", "cache read timed out, please retry")
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
