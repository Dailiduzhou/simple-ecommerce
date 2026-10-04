package biz

import (
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/errors"
)

// Immutable order-time delivery information, never resolved via AddressID.
// Repositories/cache store only ciphertext; plaintext exists at the usecase
// boundary for an owner/admin-authorized response and is excluded from JSON caches.
type OrderShippingSnapshot struct {
	ReceiverName                            string
	ReceiverPhoneEncrypt                    string
	ReceiverPhone                           string `json:"-"`
	Province, City, District, DetailAddress string
}

func (uc *orderUsecase) decryptShipping(order *Order) error {
	if order.Shipping == nil {
		return nil
	}
	phone, err := phonecrypto.DecryptPhone(order.Shipping.ReceiverPhoneEncrypt, []byte(uc.phoneSecret))
	if err != nil {
		return errors.InternalServer("ORDER_SHIPPING_DECRYPT_FAILED", "cannot read order shipping information")
	}
	copy := *order.Shipping
	copy.ReceiverPhone = phone
	order.Shipping = &copy
	return nil
}
