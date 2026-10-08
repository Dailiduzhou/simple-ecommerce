package data

import (
	"context"
	"github.com/redis/go-redis/v9"

	"github.com/go-kratos/kratos/v2/log"
)

// invalidateProductCachesForOrder drops per-product caches and bumps list
// generations for every product in the order. Call it after any transaction
// that changed product stock through the order (creation deducts stock,
// cancellation restores it), outside the transaction.
func invalidateProductCachesForOrder(ctx context.Context, data *Data, logger *log.Helper, orderID int64) {
	if data.rdb == nil {
		return
	}
	if _, batched := ctx.Value(cacheInvalidationBatchKey{}).(map[*redis.Client]*redisCacheInvalidation); !batched {
		batchCacheInvalidations(ctx, func(ctx context.Context) { invalidateProductCachesForOrder(ctx, data, logger, orderID) })
		return
	}
	rows, err := data.q.ListOrderProductCacheTargets(ctx, orderID)
	if err != nil {
		logger.WithContext(ctx).Errorw("msg", "load product cache targets failed", "order_id", orderID, "error", err)
		return
	}
	generations := []string{"product:list:gen"}
	deletes := make([]string, 0, len(rows))
	for _, row := range rows {
		generations = append(generations, redisKey("product", row.ID, "gen"), redisKey("product", "category", row.CategoryID, "gen"))
		deletes = append(deletes, redisKey("product", row.ID))
	}
	scheduleCacheInvalidation(ctx, data.rdb, logger, generations, deletes)
}
