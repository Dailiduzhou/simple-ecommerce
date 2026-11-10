package biz

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/go-kratos/kratos/v2/errors"
)

// ValidateShippingAddress is shared by address writes and checkout's locked
// snapshot. Limits count characters like PostgreSQL VARCHAR(n), not UTF-8 bytes.
func ValidateShippingAddress(receiverName, receiverPhone, province, city, district, detailAddress, addressTag string) error {
	for _, field := range []struct {
		name, value string
		max         int
		required    bool
	}{
		{"receiver_name", receiverName, 50, true},
		{"province", province, 30, true},
		{"city", city, 30, true},
		{"district", district, 30, true},
		{"detail_address", detailAddress, 255, true},
		{"address_tag", addressTag, 20, false},
	} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max ||
			(field.required && strings.TrimSpace(field.value) == "") ||
			strings.ContainsFunc(field.value, unicode.IsControl) {
			return errors.BadRequest("SHIPPING_ADDRESS_INVALID", fmt.Sprintf("%s is empty, too long or contains control characters", field.name))
		}
	}
	// Match the application's existing mainland-China mobile-number policy.
	if !IsValidCNMobile(receiverPhone) {
		return userv1.ErrorInvalidPhone("invalid receiver phone number")
	}
	return nil
}
