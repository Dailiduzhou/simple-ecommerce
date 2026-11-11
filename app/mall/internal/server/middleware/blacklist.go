package middleware

import (
	"context"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/middleware"
	kratosjwt "github.com/go-kratos/kratos/v2/middleware/auth/jwt"
)

// Authenticate turns a signed token into an authorized principal. Validation
// (signature, expiry, account existence, credential version and live session)
// happens BEFORE the claims are published on the context, so downstream
// handlers can never observe an unvalidated role. ValidateAccount also
// overwrites the token role with the authoritative database value.
func Authenticate(authUc biz.AuthUsecase) middleware.Middleware {
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

			if err := authUc.ValidateAccount(ctx, ec); err != nil {
				return nil, userv1.ErrorUnauthorized("account validation failed")
			}
			// Signature/expiry and account validation still apply to logout. Only
			// the session-active check is skipped, so an ambiguous failed response
			// can be retried after Redis already committed the revocation.
			if _, logout := req.(*userv1.LogoutRequest); !logout {
				if err := authUc.ValidateSession(ctx, ec); err != nil {
					return nil, err
				}
			}
			return handler(WithClaims(ctx, ec), req)
		}
	}
}

func WithClaims(ctx context.Context, claims *biz.EcommerceClaims) context.Context {
	return biz.WithClaims(ctx, claims)
}

func ClaimsFromContext(ctx context.Context) (*biz.EcommerceClaims, bool) {
	return biz.ClaimsFromContext(ctx)
}
