package service

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
)

func authenticatedClaims(ctx context.Context) (*biz.EcommerceClaims, error) {
	claims, ok := biz.ClaimsFromContext(ctx)
	if !ok || claims == nil || claims.UserID <= 0 {
		observability.AuthorizationDenied(ctx, "resource", "unauthenticated")
		return nil, errors.Unauthorized("UNAUTHORIZED", "authentication is required")
	}
	return claims, nil
}

func requireResourceOwner(claims *biz.EcommerceClaims, requestedUserID int64) error {
	if requestedUserID != 0 && requestedUserID != claims.UserID {
		observability.AuthorizationDenied(context.Background(), "resource_owner", "owner_mismatch")
		return errors.Forbidden("FORBIDDEN", "resource does not belong to the authenticated user")
	}
	return nil
}

func requireAdmin(ctx context.Context) (*biz.EcommerceClaims, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if claims.Role != "admin" {
		observability.AuthorizationDenied(ctx, "admin", "role")
		return nil, errors.Forbidden("FORBIDDEN", "administrator role is required")
	}
	return claims, nil
}

// isAdmin reports whether the caller is an authenticated administrator without
// rejecting non-admins. Catalog reads use it to decide whether unlisted
// (status=0) products are visible, while still serving published items to
// regular users.
func isAdmin(ctx context.Context) bool {
	claims, ok := biz.ClaimsFromContext(ctx)
	return ok && claims != nil && claims.Role == "admin"
}
