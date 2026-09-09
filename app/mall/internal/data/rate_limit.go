package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	mediav1 "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/redis/go-redis/v9"
)

type RedisWriteLimiter struct {
	rdb      *redis.Client
	limits   map[string]int32
	failOpen bool
}

func NewWriteLimiter(rdb *redis.Client, c *conf.Community) (biz.WriteLimiter, error) {
	if e := conf.ValidateCommunity(c); e != nil {
		return nil, e
	}
	limits := map[string]int32{"posts": 10, "comments": 30, "uploads": 20, "interactions": 120}
	for k, v := range map[string]int32{"posts": c.GetPostsPerMinute(), "comments": c.GetCommentsPerMinute(), "uploads": c.GetUploadsPerMinute(), "interactions": c.GetInteractionsPerMinute()} {
		if v > 0 {
			limits[k] = v
		}
	}
	return &RedisWriteLimiter{rdb: rdb, limits: limits, failOpen: c.GetRateLimitFailOpen()}, nil
}

// Atomic fixed windows for both dimensions. TTL is set on the very first INCR;
// over-limit requests do not extend the window. Keys contain no raw IP addresses.
var writeLimitScript = redis.NewScript(`
local a=redis.call('INCR',KEYS[1]); if a==1 then redis.call('PEXPIRE',KEYS[1],60000) end
local b=redis.call('INCR',KEYS[2]); if b==1 then redis.call('PEXPIRE',KEYS[2],60000) end
if a>tonumber(ARGV[1]) or b>tonumber(ARGV[2]) then return 0 end
return 1`)

func (r *RedisWriteLimiter) Allow(ctx context.Context, uid int64, ip, op string) error {
	category := biz.RateLimitCategory(op)
	if category == "" {
		return nil
	}
	if uid <= 0 {
		return errors.Unauthorized("UNAUTHORIZED", "authentication is required")
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(ip)))
	keys := []string{redisKey("community", "limit", category, "user", uid), redisKey("community", "limit", category, "ip", hex.EncodeToString(hash[:16]))}
	limit := r.limits[category]
	n, e := writeLimitScript.Run(ctx, r.rdb, keys, limit, limit*5).Int()
	if e != nil {
		observability.CommunityEvent(ctx, "rate_limit", "unavailable")
		if r.failOpen {
			return nil
		}
		return errors.ServiceUnavailable("RATE_LIMIT_UNAVAILABLE", "write limiter is unavailable")
	}
	if n == 0 {
		observability.CommunityEvent(ctx, "rate_limit", "denied")
		return mediav1.ErrorRateLimited("write quota exceeded; retry after one minute")
	}
	return nil
}
