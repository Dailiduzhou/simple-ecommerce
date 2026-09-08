package data

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
)

// invalidateProductCachesForOrder drops per-product caches and bumps list
// generations for every product in the order. Call it after any transaction
// that changed product stock through the order (creation deducts stock,
// cancellation restores it), outside the transaction.
func invalidateProductCachesForOrder(ctx context.Context, data *Data, logger *log.Helper, orderID int64) {
	rows, err := data.q.ListOrderItems(ctx, orderID)
	if err != nil {
		logger.WithContext(ctx).Errorw("msg", "load order items for product cache invalidation failed", "order_id", orderID, "error", err)
		return
	}
	for _, row := range rows {
		if err := data.rdb.Unlink(ctx, redisKey("product", row.ProductID)).Err(); err != nil {
			logger.WithContext(ctx).Errorw("msg", "delete product cache failed", "product_id", row.ProductID, "error", err)
		}
		bumpCacheGeneration(ctx, data.rdb, logger, "product:list:gen")
		if product, err := data.q.GetProduct(ctx, row.ProductID); err == nil {
			bumpCacheGeneration(ctx, data.rdb, logger, redisKey("product", "category", product.CategoryID, "gen"))
		} else {
			logger.WithContext(ctx).Errorw("msg", "load product for category cache invalidation failed", "product_id", row.ProductID, "error", err)
		}
	}
}
