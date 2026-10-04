package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

// Synchronous invalidation is a fast path; PostgreSQL also records important
// projection changes in its transactional outbox for crash/failure recovery.
type redisCacheInvalidation struct {
	ctx         context.Context
	rdb         *redis.Client
	logger      *log.Helper
	generations map[string]struct{}
	deletes     map[string]struct{}
}

type cacheInvalidationBatchKey struct{}

func newCacheInvalidation(ctx context.Context, rdb *redis.Client, logger *log.Helper) *redisCacheInvalidation {
	return &redisCacheInvalidation{ctx: ctx, rdb: rdb, logger: logger,
		generations: make(map[string]struct{}), deletes: make(map[string]struct{})}
}

// All target reads and Redis writes share one total deadline, independent of
// order size and client disconnects. A batching context is not a DB transaction.
func batchCacheInvalidations(ctx context.Context, fn func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	batches := make(map[*redis.Client]*redisCacheInvalidation)
	ctx = context.WithValue(ctx, cacheInvalidationBatchKey{}, batches)
	fn(ctx)
	for _, batch := range batches {
		_ = batch.flush()
	}
}

func scheduleCacheInvalidation(ctx context.Context, rdb *redis.Client, logger *log.Helper, generations, deletes []string) {
	if rdb == nil {
		return
	}
	if batches, ok := ctx.Value(cacheInvalidationBatchKey{}).(map[*redis.Client]*redisCacheInvalidation); ok {
		batch := batches[rdb]
		if batch == nil {
			batch = newCacheInvalidation(ctx, rdb, logger)
			batches[rdb] = batch
		}
		batch.add(generations, deletes)
		return
	}
	if state, ok := ctx.Value(txStateKey{}).(*txState); ok && state != nil {
		if state.cacheInvalidations == nil {
			state.cacheInvalidations = make(map[*redis.Client]*redisCacheInvalidation)
		}
		batch := state.cacheInvalidations[rdb]
		if batch == nil {
			batch = newCacheInvalidation(context.WithoutCancel(ctx), rdb, logger)
			state.cacheInvalidations[rdb] = batch
			state.afterCommit = append(state.afterCommit, func() { _ = batch.flush() })
		}
		batch.add(generations, deletes)
		return
	}
	batch := newCacheInvalidation(context.WithoutCancel(ctx), rdb, logger)
	batch.add(generations, deletes)
	_ = batch.flush()
}

func (b *redisCacheInvalidation) add(generations, deletes []string) {
	for _, key := range generations {
		if key != "" {
			b.generations[key] = struct{}{}
		}
	}
	for _, key := range deletes {
		if key != "" {
			b.deletes[key] = struct{}{}
		}
	}
}

func (b *redisCacheInvalidation) flush() error {
	if len(b.generations)+len(b.deletes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(b.ctx, 2*time.Second)
	defer cancel()
	// Each operation shares a single timeout and connection round trip. Duplicate
	// generation bumps and entity key deletes are collapsed before reaching Redis.
	pipe := b.rdb.Pipeline()
	for key := range b.generations {
		pipe.Incr(ctx, key)
	}
	if len(b.deletes) > 0 {
		keys := make([]string, 0, len(b.deletes))
		for key := range b.deletes {
			keys = append(keys, key)
		}
		pipe.Unlink(ctx, keys...)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		key := "cache"
		if len(b.generations) > 0 {
			for k := range b.generations {
				key = k
				break
			}
		} else {
			for k := range b.deletes {
				key = k
				break
			}
		}
		cacheFailure(ctx, b.logger, "invalidate_pipeline", key, err)
		return err
	}
	return nil
}
