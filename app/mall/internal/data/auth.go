package data

import (
	"context"
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
