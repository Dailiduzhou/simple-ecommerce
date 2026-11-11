package service

import (
	"context"
	"fmt"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
)

// authenticatedActor returns the principal whose claims were validated by the
// transport middleware on this request. Handlers never build an Actor from
// request fields.
func authenticatedActor(ctx context.Context) (biz.Actor, error) {
	claims, ok := biz.ClaimsFromContext(ctx)
	if !ok || claims == nil || claims.UserID <= 0 {
		observability.AuthorizationDenied(ctx, "resource", "unauthenticated")
		return biz.Actor{}, errors.Unauthorized("UNAUTHORIZED", "authentication is required")
	}
	actor := biz.Actor{ID: claims.UserID, Role: claims.Role}
	if err := actor.Validate(); err != nil {
		observability.AuthorizationDenied(ctx, "resource", "invalid_role")
		return biz.Actor{}, err
	}
	return actor, nil
}

// requireCapability gates role eligibility. Resource ownership is a separate
// check below, because the role alone cannot prove who owns a row.
func requireCapability(ctx context.Context, capability biz.Capability) (biz.Actor, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return biz.Actor{}, err
	}
	if !actor.Allows(capability) {
		observability.AuthorizationDenied(ctx, "capability", string(actor.Role))
		return biz.Actor{}, errors.Forbidden("FORBIDDEN", fmt.Sprintf("role %q is not allowed to perform this operation", actor.Role))
	}
	return actor, nil
}

// requireAdmin is shorthand for administrator-only platform operations.
func requireAdmin(ctx context.Context) (biz.Actor, error) {
	return requireCapability(ctx, biz.CapManagePlatform)
}

// requireResourceOwner keeps a request path/body user_id honest. Zero means
// "the caller" and is accepted for self-service routes.
func requireResourceOwner(ctx context.Context, actor biz.Actor, requestedUserID int64) error {
	if requestedUserID != 0 && requestedUserID != actor.ID {
		observability.AuthorizationDenied(ctx, "resource_owner", "owner_mismatch")
		return errors.Forbidden("FORBIDDEN", "resource does not belong to the authenticated user")
	}
	return nil
}

// requireSelfOrAdmin is the back-office variant: administrators may read or
// modify another account, ordinary users only their own.
func requireSelfOrAdmin(ctx context.Context, actor biz.Actor, requestedUserID int64) error {
	if requestedUserID == 0 || requestedUserID == actor.ID {
		return nil
	}
	if actor.IsAdmin() {
		return nil
	}
	observability.AuthorizationDenied(ctx, "resource_owner", "owner_mismatch")
	return errors.Forbidden("FORBIDDEN", "resource does not belong to the authenticated user")
}
