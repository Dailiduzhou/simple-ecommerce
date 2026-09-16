package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

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

func NewWriteLimiter(rdb *redis.Client, c *conf.Community, a *conf.Auth) (biz.WriteLimiter, error) {
	if e := conf.ValidateCommunity(c); e != nil {
		return nil, e
	}
	if e := validateAuthLimits(a); e != nil {
		return nil, e
	}
	limits := map[string]int32{"posts": 10, "comments": 30, "uploads": 20, "interactions": 120, "auth": 10}
	for k, v := range map[string]int32{"posts": c.GetPostsPerMinute(), "comments": c.GetCommentsPerMinute(), "uploads": c.GetUploadsPerMinute(), "interactions": c.GetInteractionsPerMinute()} {
		if v > 0 {
			limits[k] = v
		}
	}
	if v := a.GetAuthRequestsPerMinute(); v > 0 {
		limits["auth"] = v
	}
	return &RedisWriteLimiter{rdb: rdb, limits: limits, failOpen: c.GetRateLimitFailOpen()}, nil
}

// validateAuthLimits mirrors conf.ValidateCommunity: zero means the documented
// default, while negative or absurd values must fail boot instead of silently
// unthrottling login/register/refresh.
func validateAuthLimits(a *conf.Auth) error {
	if v := a.GetAuthRequestsPerMinute(); v < 0 || v > 10000 {
		return fmt.Errorf("auth.auth_requests_per_minute must be between 0 (default) and 10000")
	}
	if v := a.GetLoginMaxAttempts(); v < 0 || v > 100 {
		return fmt.Errorf("auth.login_max_attempts must be between 0 (default) and 100")
	}
	if d := a.GetLoginLockoutDuration().AsDuration(); d < 0 || d > 24*time.Hour {
		return fmt.Errorf("auth.login_lockout_duration must be between 0 (default) and 24h")
	}
	return nil
}

// Atomic fixed windows for both dimensions. TTL is set on the very first INCR;
// over-limit requests do not extend the window. Keys contain no raw IP addresses.
var writeLimitScript = redis.NewScript(`
local a=redis.call('INCR',KEYS[1]); if a==1 then redis.call('PEXPIRE',KEYS[1],60000) end
local b=redis.call('INCR',KEYS[2]); if b==1 then redis.call('PEXPIRE',KEYS[2],60000) end
if a>tonumber(ARGV[1]) or b>tonumber(ARGV[2]) then return 0 end
return 1`)

// Single-dimension window for anonymous auth requests (login/register/refresh
// arrive without claims), with the same fixed-window semantics.
var anonLimitScript = redis.NewScript(`
local a=redis.call('INCR',KEYS[1]); if a==1 then redis.call('PEXPIRE',KEYS[1],60000) end
if a>tonumber(ARGV[1]) then return 0 end
return 1`)

func (r *RedisWriteLimiter) Allow(ctx context.Context, uid int64, ip, op string) error {
	category := biz.RateLimitCategory(op)
	if category == "" {
		return nil
	}
	// Only the auth bucket is reachable without an identity: login, register
	// and refresh are JWT-whitelisted and are throttled on the IP dimension
	// alone. Every other bucket still requires claims.
	if uid <= 0 && category != "auth" {
		return errors.Unauthorized("UNAUTHORIZED", "authentication is required")
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(ip)))
	ipKey := redisKey("community", "limit", category, "ip", hex.EncodeToString(hash[:16]))
	limit := r.limits[category]
	var n int
	var e error
	if uid <= 0 {
		n, e = anonLimitScript.Run(ctx, r.rdb, []string{ipKey}, limit).Int()
	} else {
		// Defensive only: at the transport layer the auth operations are
		// JWT-whitelisted, so the selector skips InjectClaims and requests
		// always arrive without claims. Direct callers keep the dual
		// user+IP dimension.
		n, e = writeLimitScript.Run(ctx, r.rdb, []string{redisKey("community", "limit", category, "user", uid), ipKey}, limit, limit*5).Int()
	}
	if e != nil {
		return r.unavailable(ctx, category)
	}
	if n == 0 {
		observability.CommunityEvent(ctx, "rate_limit", "denied")
		return mediav1.ErrorRateLimited("write quota exceeded; retry after one minute")
	}
	return nil
}

func (r *RedisWriteLimiter) unavailable(ctx context.Context, category string) error {
	observability.CommunityEvent(ctx, "rate_limit", "unavailable")
	// The auth bucket stays fail-closed like the JWT blacklist chain:
	// community.rate_limit_fail_open is an availability knob for social
	// writes and must never unthrottle login/register/refresh.
	if category == "auth" {
		return errors.ServiceUnavailable("RATE_LIMIT_UNAVAILABLE", "write limiter is unavailable")
	}
	if r.failOpen {
		return nil
	}
	return errors.ServiceUnavailable("RATE_LIMIT_UNAVAILABLE", "write limiter is unavailable")
}
