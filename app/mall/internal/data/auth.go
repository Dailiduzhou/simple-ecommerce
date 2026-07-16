package data

import (
	"context"
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
