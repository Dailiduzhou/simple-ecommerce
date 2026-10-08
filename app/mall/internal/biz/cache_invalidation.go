package biz

import "context"

const CacheInvalidationJobKind = "cache_invalidation"

type CacheInvalidationArgs struct{}

func (CacheInvalidationArgs) Kind() string { return CacheInvalidationJobKind }

// CacheInvalidationRepo drains durable projection changes. Redis application
// may repeat after a lost commit acknowledgement; generation increments are
// safe to repeat, and database rows disappear only after Redis acknowledges.
type CacheInvalidationRepo interface {
	FlushCacheInvalidations(context.Context, int32) (int, error)
}
