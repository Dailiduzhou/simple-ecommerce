package data

import (
	"context"
	stderrors "errors"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/jackc/pgx/v5"
)

// withAccountMutation makes the last-admin check authoritative. The usecase's
// precheck is only advisory: profiles may be cached and concurrent requests may
// both have observed several administrators. All role changes and deletions
// acquire this shared transaction lock BEFORE any user/content row locks, then
// re-read the account and administrator count directly from the database.
func (r *UserRepo) withAccountMutation(ctx context.Context, id int64, removesAdmin bool, fn func(context.Context) error) error {
	mutate := func(ctx context.Context) error {
		q := r.data.DB(ctx)
		if err := q.LockUserRoleChanges(ctx); err != nil {
			return err
		}
		current, err := q.GetUserByID(ctx, id)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return userv1.ErrorUserNotFound("user %d not found", id)
		}
		if err != nil {
			return err
		}
		if removesAdmin && current.Role == string(biz.RoleAdmin) {
			admins, err := q.CountAdmins(ctx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return biz.ErrLastAdmin
			}
		}
		return fn(ctx)
	}
	if inTransaction(ctx) {
		// Community cleanup owns the transaction; never open a nested one.
		return mutate(ctx)
	}
	return r.tx.InTx(ctx, mutate)
}
