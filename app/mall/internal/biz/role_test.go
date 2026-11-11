package biz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRoleIsClosedSet(t *testing.T) {
	for _, raw := range []string{"user", "merchant", "admin"} {
		role, err := ParseRole(raw)
		require.NoError(t, err)
		require.Equal(t, Role(raw), role)
		require.True(t, role.Valid())
	}
	for _, raw := range []string{"", "Admin", "administrator", "merchant ", "root"} {
		_, err := ParseRole(raw)
		require.Error(t, err, "unknown role %q must be rejected", raw)
		require.False(t, Role(raw).Valid())
	}
}

func TestRoleCapabilities(t *testing.T) {
	for _, capability := range []Capability{
		CapManageCatalog, CapShipOrder, CapRefundPayment, CapReconcilePayment,
		CapModerateContent, CapManageUsers, CapManageRoles, CapManagePlatform,
	} {
		require.True(t, RoleAdmin.Allows(capability), "admin must allow %v", capability)
	}
	for _, capability := range []Capability{
		CapManageCatalog, CapShipOrder, CapRefundPayment,
	} {
		require.True(t, RoleMerchant.Allows(capability), "merchant must allow %v", capability)
	}
	for _, capability := range []Capability{
		CapReconcilePayment, CapModerateContent, CapManageUsers, CapManageRoles, CapManagePlatform,
	} {
		require.False(t, RoleMerchant.Allows(capability), "merchant must not allow %v", capability)
	}
	for _, capability := range []Capability{
		CapManageCatalog, CapShipOrder, CapRefundPayment, CapReconcilePayment,
		CapModerateContent, CapManageUsers, CapManageRoles, CapManagePlatform,
	} {
		require.False(t, RoleUser.Allows(capability), "user must not allow %v", capability)
	}
}

func TestActorMerchantOwnership(t *testing.T) {
	admin := Actor{ID: 1, Role: RoleAdmin}
	owner := Actor{ID: 2, Role: RoleMerchant}
	user := Actor{ID: 4, Role: RoleUser}

	require.True(t, admin.CanManageMerchant(2))
	require.True(t, admin.CanManageMerchant(0), "administrators act for every merchant")
	require.True(t, owner.CanManageMerchant(2))
	require.False(t, owner.CanManageMerchant(3))
	require.False(t, owner.CanManageMerchant(0), "the zero merchant is never a real owner")
	require.False(t, user.CanManageMerchant(4), "ordinary users are not merchants")

	require.NoError(t, owner.RequireMerchant(2))
	require.Error(t, owner.RequireMerchant(3))
	require.Error(t, owner.RequireMerchant(0))

	require.NoError(t, owner.Require(CapManageCatalog))
	require.Error(t, user.Require(CapManageCatalog))
	require.NoError(t, admin.Require(CapManagePlatform))
}

func TestActorValidationRequiresKnownRole(t *testing.T) {
	require.Error(t, (Actor{ID: 1}).Validate())
	require.Error(t, (Actor{Role: RoleAdmin}).Validate())
	require.Error(t, (Actor{ID: 1, Role: Role("root")}).Validate())
	require.NoError(t, (Actor{ID: 1, Role: RoleUser}).Validate())
}

func TestActorOwnsAdminException(t *testing.T) {
	admin := Actor{ID: 99, Role: RoleAdmin}
	require.NoError(t, admin.Owns(1, true))
	require.Error(t, admin.Owns(1, false))
	require.NoError(t, (Actor{ID: 1, Role: RoleUser}).Owns(1, false))
	require.Error(t, (Actor{ID: 1, Role: RoleUser}).Owns(2, true))
}
