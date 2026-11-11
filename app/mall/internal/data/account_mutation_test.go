package data

import (
	"context"
	"testing"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestAccountMutationChecksAuthoritativeRoleUnderSharedLock(t *testing.T) {
	for _, operation := range []string{"demote", "delete"} {
		for _, admins := range []int64{1, 2} {
			t.Run(operation+"/"+map[int64]string{1: "last admin", 2: "several admins"}[admins], func(t *testing.T) {
				ctx := context.Background()
				q := mockdb.NewMockQuerier(gomock.NewController(t))
				mr := miniredis.RunT(t)
				r := NewUserRepo(newTestData(t, q, mr), log.DefaultLogger)
				r.tx = testTxManager{q: q}
				// A stale public profile must never bypass the authoritative check.
				r.setCache(ctx, redisKey("user", 7, "g", 0), &biz.UserProfile{ID: 7, Role: biz.RoleUser})
				calls := []*gomock.Call{
					q.EXPECT().LockUserRoleChanges(gomock.Any()).Return(nil),
					q.EXPECT().GetUserByID(gomock.Any(), int64(7)).Return(db.User{ID: 7, Role: "admin"}, nil),
					q.EXPECT().CountAdmins(gomock.Any()).Return(admins, nil),
				}
				if admins > 1 {
					if operation == "demote" {
						calls = append(calls, q.EXPECT().UpdateUserRole(gomock.Any(), db.UpdateUserRoleParams{ID: 7, Role: "merchant"}).Return(nil))
					} else {
						calls = append(calls, q.EXPECT().DeleteUser(gomock.Any(), int64(7)).Return(nil))
					}
				}
				gomock.InOrder(calls...)
				var err error
				if operation == "demote" {
					err = r.UpdateUserRole(ctx, 7, biz.RoleMerchant)
				} else {
					err = r.DeleteUser(ctx, 7)
				}
				if admins == 1 {
					require.ErrorIs(t, err, biz.ErrLastAdmin)
					require.False(t, mr.Exists(userGenerationKey(7)), "rejected writes cannot invalidate caches")
				} else {
					require.NoError(t, err)
					require.Equal(t, "1", r.data.rdb.Get(ctx, userGenerationKey(7)).Val())
				}
			})
		}
	}
}

func TestAccountMutationRejectsDeletedTargetAndLockFailure(t *testing.T) {
	for _, lockFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing target", true: "lock failure"}[lockFailure], func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			r := NewUserRepo(&Data{q: q}, log.DefaultLogger)
			r.tx = testTxManager{q: q}
			if lockFailure {
				q.EXPECT().LockUserRoleChanges(gomock.Any()).Return(context.Canceled)
			} else {
				gomock.InOrder(
					q.EXPECT().LockUserRoleChanges(gomock.Any()).Return(nil),
					q.EXPECT().GetUserByID(gomock.Any(), int64(7)).Return(db.User{}, pgx.ErrNoRows),
				)
			}
			err := r.UpdateUserRole(context.Background(), 7, biz.RoleAdmin)
			if lockFailure {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.True(t, userv1.IsUserNotFound(err))
			}
		})
	}
}

func TestRoleChangeDefersInvalidationUntilCommit(t *testing.T) {
	ctx := context.Background()
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	r := NewUserRepo(newTestData(t, q, mr), log.DefaultLogger)
	state := &txState{}
	ctx = context.WithValue(WithQuerier(ctx, q, nil), txStateKey{}, state)
	gomock.InOrder(
		q.EXPECT().LockUserRoleChanges(gomock.Any()).Return(nil),
		q.EXPECT().GetUserByID(gomock.Any(), int64(7)).Return(db.User{ID: 7, Role: "user"}, nil),
		q.EXPECT().UpdateUserRole(gomock.Any(), db.UpdateUserRoleParams{ID: 7, Role: "admin"}).Return(nil),
	)
	require.NoError(t, r.UpdateUserRole(ctx, 7, biz.RoleAdmin))
	require.False(t, mr.Exists(userGenerationKey(7)))
	require.Len(t, state.afterCommit, 1)
	state.afterCommit[0]()
	require.Equal(t, "1", r.data.rdb.Get(ctx, userGenerationKey(7)).Val())
}
