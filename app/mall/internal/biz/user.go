package biz

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type UserRepo interface{}

type AuthRepo interface {
	SetBlacklist(ctx context.Context, tokenID string, expiration time.Duration)
	IsBlacklisted(ctx context.Context, tokenID string) bool
}

type EcommerceClaims struct {
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}
