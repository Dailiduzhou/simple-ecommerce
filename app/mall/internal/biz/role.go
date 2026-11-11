package biz

import (
	"fmt"

	"github.com/go-kratos/kratos/v2/errors"
)

// Role is the platform-level identity of an account. It is intentionally a
// closed set: the database enforces the same list, and tokens carrying any
// other value are rejected instead of silently downgrading to a normal user.
type Role string

const (
	RoleUser     Role = "user"
	RoleMerchant Role = "merchant"
	RoleAdmin    Role = "admin"
)

// Capability is an action class. Role eligibility is decided here once, while
// ownership of the concrete resource is decided by the caller that loaded it.
type Capability int

const (
	// CapManageCatalog covers product create/update/status/delete/stock.
	// Merchants may act only on their own products.
	CapManageCatalog Capability = iota
	// CapShipOrder covers paid -> shipped. Merchants may ship their own orders.
	CapShipOrder
	// CapRefundPayment covers refunding a settled payment for an owned merchant.
	CapRefundPayment
	// CapReconcilePayment covers manual payment reconciliation.
	CapReconcilePayment
	// CapModerateContent covers deleting somebody else's posts and comments.
	CapModerateContent
	// CapManageUsers covers reading, updating and deleting other accounts.
	CapManageUsers
	// CapManageRoles covers changing an account's role.
	CapManageRoles
	// CapManagePlatform covers platform-level catalog (categories, marketing
	// events), MQ administration and other back-office-only operations.
	CapManagePlatform
)

func ParseRole(raw string) (Role, error) {
	switch role := Role(raw); role {
	case RoleUser, RoleMerchant, RoleAdmin:
		return role, nil
	default:
		return "", fmt.Errorf("unknown role %q", raw)
	}
}

func (r Role) Valid() bool {
	_, err := ParseRole(string(r))
	return err == nil
}

func (r Role) IsAdmin() bool    { return r == RoleAdmin }
func (r Role) IsMerchant() bool { return r == RoleMerchant }

// Allows decides role eligibility only. A merchant still has to prove the
// product/order/payment belongs to them.
func (r Role) Allows(c Capability) bool {
	if r == RoleAdmin {
		return true
	}
	if r != RoleMerchant {
		return false
	}
	switch c {
	case CapManageCatalog, CapShipOrder, CapRefundPayment:
		return true
	default:
		return false
	}
}

// Actor is the authenticated principal plus the role refreshed from the
// database on this request. Never build it from request bodies.
type Actor struct {
	ID   int64
	Role Role
}

func (a Actor) Validate() error {
	if a.ID <= 0 {
		return errors.Unauthorized("UNAUTHORIZED", "authentication is required")
	}
	if !a.Role.Valid() {
		return errors.Unauthorized("UNAUTHORIZED", "account role is invalid")
	}
	return nil
}

func (a Actor) IsAdmin() bool { return a.Role.IsAdmin() }

func (a Actor) Allows(c Capability) bool { return a.Role.Allows(c) }

// Require returns 401 for a missing principal and 403 for insufficient role.
func (a Actor) Require(c Capability) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if !a.Allows(c) {
		return errors.Forbidden("FORBIDDEN", fmt.Sprintf("role %q is not allowed to perform this operation", a.Role))
	}
	return nil
}

// Owns checks user-owned resources (posts, comments, addresses, orders...).
func (a Actor) Owns(id int64, allowAdmin bool) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.ID != id && !(allowAdmin && a.IsAdmin()) {
		return errors.Forbidden("FORBIDDEN", "resource belongs to another user")
	}
	return nil
}

// CanManageMerchant reports whether the actor may act for the merchant that
// owns a resource. Administrators act for every merchant; a merchant only for
// themselves. merchantID == 0 never matches a normal account.
func (a Actor) CanManageMerchant(merchantID int64) bool {
	if err := a.Validate(); err != nil {
		return false
	}
	if a.Role.IsAdmin() {
		return true
	}
	return a.Role.IsMerchant() && merchantID > 0 && merchantID == a.ID
}

func (a Actor) RequireMerchant(merchantID int64) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if !a.CanManageMerchant(merchantID) {
		return errors.Forbidden("FORBIDDEN", "resource belongs to another merchant")
	}
	return nil
}
