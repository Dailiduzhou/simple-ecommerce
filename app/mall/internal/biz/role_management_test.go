package biz

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestUpdateUserRoleAuthorizationAndSafeguards(t *testing.T) {
	ctx := context.Background()
	admin := Actor{ID: 1, Role: RoleAdmin}
	merchant := Actor{ID: 2, Role: RoleMerchant}
	user := Actor{ID: 3, Role: RoleUser}

	targetRole := RoleUser
	changed := int64(0)
	admins := int64(1)
	repo := &fakeUserRepo{
		getUserByID: func(_ context.Context, id int64) (*User, error) {
			return &User{ID: id, Role: targetRole}, nil
		},
		updateUserRole: func(_ context.Context, id int64, role Role) error {
			changed, targetRole = id, role
			return nil
		},
		countAdmins: func(context.Context) (int64, error) { return admins, nil },
	}
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

	// Only administrators may hand out roles.
	_, err := uc.UpdateUserRole(ctx, merchant, 3, string(RoleMerchant))
	require.Equal(t, int32(403), errors.FromError(err).Code)
	_, err = uc.UpdateUserRole(ctx, user, 3, string(RoleAdmin))
	require.Equal(t, int32(403), errors.FromError(err).Code)
	require.Zero(t, changed)

	// The closed role set is enforced before any write.
	_, err = uc.UpdateUserRole(ctx, admin, 3, "root")
	require.Equal(t, int32(400), errors.FromError(err).Code)
	require.Zero(t, changed)

	// An administrator cannot demote themselves, which would lock the platform.
	targetRole = RoleAdmin
	_, err = uc.UpdateUserRole(ctx, admin, 1, string(RoleUser))
	require.ErrorIs(t, err, ErrRoleChangeSelf)
	require.Zero(t, changed)

	// The last administrator cannot be demoted by another administrator.
	targetRole = RoleAdmin
	otherAdmin := Actor{ID: 99, Role: RoleAdmin}
	admins = 1
	_, err = uc.UpdateUserRole(ctx, otherAdmin, 1, string(RoleUser))
	require.ErrorIs(t, err, ErrLastAdmin)
	require.Zero(t, changed)

	// Demoting one of several administrators is allowed.
	admins = 2
	_, err = uc.UpdateUserRole(ctx, otherAdmin, 1, string(RoleMerchant))
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	require.Equal(t, RoleMerchant, targetRole)

	// Promoting an ordinary account is the supported merchant onboarding path.
	targetRole, changed = RoleUser, 0
	profile, err := uc.UpdateUserRole(ctx, admin, 3, string(RoleMerchant))
	require.NoError(t, err)
	require.Equal(t, int64(3), changed)
	require.Equal(t, RoleMerchant, targetRole)
	require.Equal(t, RoleMerchant, profile.Role)
}

func TestDeleteUserAccountPolicy(t *testing.T) {
	ctx := context.Background()
	admin := Actor{ID: 1, Role: RoleAdmin}
	otherAdmin := Actor{ID: 2, Role: RoleAdmin}
	user := Actor{ID: 3, Role: RoleUser}

	target := &User{ID: 9, Role: RoleUser}
	admins := int64(2)
	deleted := int64(0)
	repo := &fakeUserRepo{
		getUserByID: func(context.Context, int64) (*User, error) { return target, nil },
		countAdmins: func(context.Context) (int64, error) { return admins, nil },
		deleteUser:  func(_ context.Context, id int64) error { deleted = id; return nil },
	}
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

	err := uc.DeleteUser(ctx, user, target.ID)
	require.Equal(t, int32(403), errors.FromError(err).Code, "users cannot delete other accounts")

	require.NoError(t, uc.DeleteUser(ctx, admin, target.ID))
	require.Equal(t, target.ID, deleted)

	// The last administrator cannot be removed by themselves or by a peer.
	target = &User{ID: 1, Role: RoleAdmin}
	admins = 1
	deleted = 0
	err = uc.DeleteUser(ctx, admin, 1)
	require.ErrorIs(t, err, ErrLastAdmin)
	err = uc.DeleteUser(ctx, otherAdmin, 1)
	require.ErrorIs(t, err, ErrLastAdmin)
	require.Zero(t, deleted)

	// With several administrators the delete goes through.
	admins = 2
	require.NoError(t, uc.DeleteUser(ctx, otherAdmin, 1))
	require.Equal(t, int64(1), deleted)
}
