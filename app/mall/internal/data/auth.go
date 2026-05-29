package data

import (
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
)

type AuthRepo struct {
	rdb *redis.Client
	log *log.Helper
}

func NewAuthRepo(rdb *redis.Client, logger log.Logger) *AuthRepo {
	return &AuthRepo{rdb: rdb, log: log.NewHelper(logger)}
}
