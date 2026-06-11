package middleware

import (
	"context"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	kratosjwt "github.com/go-kratos/kratos/v2/middleware/auth/jwt"
	"github.com/go-kratos/kratos/v2/middleware"
)

func CheckBlacklist(authUc biz.AuthUsecase) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			claims, ok := kratosjwt.FromContext(ctx)
			if !ok {
				return nil, userv1.ErrorUnauthorized("missing token")
			}

			ec, ok := claims.(*biz.EcommerceClaims)
			if !ok {
				return nil, userv1.ErrorUnauthorized("invalid token claims")
			}

			if ec.ID == "" {
				return nil, userv1.ErrorUnauthorized("missing token id")
			}

			blacklisted, err := authUc.IsTokenBlacklisted(ctx, ec.ID)
			if err != nil {
				return nil, userv1.ErrorUnauthorized("check blacklist failed")
			}
			if blacklisted {
				return nil, userv1.ErrorTokenExpired("token has been revoked")
			}

			return handler(ctx, req)
		}
	}
}

type keyClaims struct{}

func WithClaims(ctx context.Context, claims *biz.EcommerceClaims) context.Context {
	return context.WithValue(ctx, keyClaims{}, claims)
}

func ClaimsFromContext(ctx context.Context) (*biz.EcommerceClaims, bool) {
	claims, ok := ctx.Value(keyClaims{}).(*biz.EcommerceClaims)
	return claims, ok
}

func InjectClaims() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if claims, ok := kratosjwt.FromContext(ctx); ok {
				if ec, ok := claims.(*biz.EcommerceClaims); ok {
					ctx = WithClaims(ctx, ec)
				}
			}
			return handler(ctx, req)
		}
	}
}
