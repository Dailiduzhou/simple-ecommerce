package data

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

// Session and used-JTI keys share a Redis Cluster hash tag. All mutations are
// atomic, and revocation never extends the original fixed session lifetime.
func sessionKey(id string) string { return redisKey("auth", "session", "{"+id+"}") }

var createSessionScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], 'owner', ARGV[1], 'state', 'active')
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1`)

func (r *AuthRepo) CreateSession(ctx context.Context, id string, userID int64, expiration time.Duration) error {
	if id == "" || userID <= 0 || expiration.Milliseconds() <= 0 {
		return fmt.Errorf("invalid session")
	}
	n, err := createSessionScript.Run(ctx, r.rdb, []string{sessionKey(id)}, userID, expiration.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("session already exists")
	}
	return nil
}

func (r *AuthRepo) SessionActive(ctx context.Context, id string, userID int64) (bool, error) {
	values, err := r.rdb.HMGet(ctx, sessionKey(id), "owner", "state").Result()
	if err != nil {
		return false, err
	}
	return values[0] == strconv.FormatInt(userID, 10) && values[1] == "active", nil
}

var consumeSessionRefreshScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'owner') ~= ARGV[1] or redis.call('HGET', KEYS[1], 'state') ~= 'active' then return 0 end
local ttl = redis.call('PTTL', KEYS[1])
if ttl <= 0 then return 0 end
ttl = math.min(ttl, tonumber(ARGV[2]))
if redis.call('SET', KEYS[2], '1', 'NX', 'PX', ttl) then return 1 end
return 0`)

func (r *AuthRepo) ConsumeSessionRefresh(ctx context.Context, id string, userID int64, tokenID string, expiration time.Duration) (bool, error) {
	if id == "" || userID <= 0 || tokenID == "" || expiration.Milliseconds() <= 0 {
		return false, nil
	}
	n, err := consumeSessionRefreshScript.Run(ctx, r.rdb,
		[]string{sessionKey(id), redisKey("auth", "session", "{"+id+"}", "used", tokenID)},
		userID, expiration.Milliseconds()).Int()
	return n == 1, err
}

var revokeSessionScript = redis.NewScript(`
local owner = redis.call('HGET', KEYS[1], 'owner')
if not owner then return 1 end
if owner ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], 'state', 'revoked')
return 1`)

func (r *AuthRepo) RevokeSession(ctx context.Context, id string, userID int64) (bool, error) {
	if id == "" || userID <= 0 {
		return false, nil
	}
	n, err := revokeSessionScript.Run(ctx, r.rdb, []string{sessionKey(id)}, userID).Int()
	return n == 1, err
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
	if err := r.rdb.Unlink(ctx, key).Err(); err != nil {
		r.log.Errorf("clear login failures failed: %v", err)
		return err
	}
	return nil
}
