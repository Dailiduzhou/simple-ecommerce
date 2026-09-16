package data

import (
	"context"
	"errors"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

var _ biz.AuthRepo = (*AuthRepo)(nil)

type AuthRepo struct {
	rdb *redis.Client
	log *log.Helper
}

func NewAuthRepo(rdb *redis.Client, logger log.Logger) *AuthRepo {
	return &AuthRepo{rdb: rdb, log: log.NewHelper(logger)}
}

func (r *AuthRepo) SetBlacklist(ctx context.Context, tokenID string, expiration time.Duration) error {
	key := redisKey("jwt", "blacklist", tokenID)
	if err := r.rdb.Set(ctx, key, "1", expiration).Err(); err != nil {
		r.log.Errorf("set blacklist failed: %v", err)
		return err
	}
	return nil
}

func (r *AuthRepo) IsBlacklisted(ctx context.Context, tokenID string) (bool, error) {
	key := redisKey("jwt", "blacklist", tokenID)
	exists, err := r.rdb.Exists(ctx, key).Result()
	if err != nil {
		r.log.Errorf("check blacklist failed: %v", err)
		return false, err
	}
	return exists > 0, nil
}

func (r *AuthRepo) ConsumeRefresh(ctx context.Context, tokenID string, expiration time.Duration) (bool, error) {
	if expiration <= 0 {
		return false, nil
	}
	return r.rdb.SetNX(ctx, redisKey("jwt", "blacklist", tokenID), "1", expiration).Result()
}

// Fixed window armed on the first failure: only the initial INCR sets the
// expiry, so repeated probing cannot extend the lockout.
var loginFailureScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n`)

// RecordLoginFailure counts one failed login attempt for a phone hash (an
// HMAC-SHA256 digest in base64 encoding, no PII). The window is fixed from the first failure.
func (r *AuthRepo) RecordLoginFailure(ctx context.Context, phoneHash string, window time.Duration) error {
	if window <= 0 {
		return errors.New("login lockout window must be positive")
	}
	key := redisKey("auth", "login", "fail", phoneHash)
	if _, err := loginFailureScript.Run(ctx, r.rdb, []string{key}, window.Milliseconds()).Result(); err != nil {
		r.log.Errorf("record login failure failed: %v", err)
		return err
	}
	return nil
}

func (r *AuthRepo) LoginFailures(ctx context.Context, phoneHash string) (int64, error) {
	key := redisKey("auth", "login", "fail", phoneHash)
	n, err := r.rdb.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		r.log.Errorf("get login failures failed: %v", err)
		return 0, err
	}
	return n, nil
}

func (r *AuthRepo) ClearLoginFailures(ctx context.Context, phoneHash string) error {
	key := redisKey("auth", "login", "fail", phoneHash)
	if err := r.rdb.Del(ctx, key).Err(); err != nil {
		r.log.Errorf("clear login failures failed: %v", err)
		return err
	}
	return nil
}
