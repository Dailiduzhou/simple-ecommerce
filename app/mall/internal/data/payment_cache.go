package data

import (
	"context"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/redis/go-redis/v9"
)

func (r *PaymentRepo) getCache(ctx context.Context, key string) (*biz.PaymentDO, error) {
	payment, err := readJSONCache[*biz.PaymentDO](ctx, r.data, key)
	// Payment lookups have an explicit not-found error, not a nil success.
	if err == nil && payment == nil {
		return nil, redis.Nil
	}
	return payment, err
}

func (r *PaymentRepo) setCache(ctx context.Context, key string, payment *biz.PaymentDO) {
	writeJSONCache(ctx, r.data, r.log, key, payment, 15*time.Minute)
}

func paymentCacheKeysFor(payment db.Payment, gen int64) []string {
	keys := []string{
		redisKey("payment", payment.ID, "g", gen),
		redisKey("payment", "order", payment.OrderID, "g", gen),
		redisKey("payment", "order", payment.OrderID, "active", payment.PayChannel, "g", gen),
	}
	if payment.OutTradeNo != "" {
		keys = append(keys, redisKey("payment", "out_trade_no", payment.OutTradeNo, "g", gen))
	}
	return keys
}

func paymentGenerationKeys(payment db.Payment) []string {
	return []string{redisKey("payment", payment.ID, "gen"), redisKey("payment", "order", payment.OrderID, "gen"), redisKey("payment", "out_trade_no", payment.OutTradeNo, "gen")}
}

func (r *PaymentRepo) invalidatePayment(ctx context.Context, payment db.Payment) {
	scheduleCacheInvalidation(ctx, r.data.rdb, r.log, paymentGenerationKeys(payment), nil)
}

func (r *PaymentRepo) invalidateOrder(ctx context.Context, orderID int64) {
	order, err := querierFromContext(ctx, r.data.q).GetOrder(ctx, orderID)
	if err != nil {
		return
	}
	deletes := []string{redisKey("order", orderID), redisKey("order", "user", orderID, order.UserID)}
	if order.OutTradeNo != "" {
		deletes = append(deletes, redisKey("order", "no", order.OutTradeNo))
	}
	scheduleCacheInvalidation(ctx, r.data.rdb, r.log, []string{
		redisKey("order", "user", order.UserID, "gen"), redisKey("order", "user", "ongoing", order.UserID, "gen"),
	}, deletes)
}

func (r *PaymentRepo) deleteCache(ctx context.Context, key string) {
	deleteJSONCache(ctx, r.data, r.log, key)
}
