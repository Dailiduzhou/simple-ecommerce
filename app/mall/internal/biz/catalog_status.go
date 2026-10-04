package biz

import "github.com/go-kratos/kratos/v2/errors"

type ProductStatus int16

const (
	ProductOffShelf ProductStatus = iota
	ProductOnShelf
)

type EventStatus int16

const (
	EventUpcoming EventStatus = iota
	EventActive
	EventEnded
)

func ValidateProductStatus(status int32) error {
	if status != int32(ProductOffShelf) && status != int32(ProductOnShelf) {
		return errors.BadRequest("PRODUCT_STATUS_INVALID", "product status must be 0 or 1")
	}
	return nil
}
func ValidateEventStatus(status int32) error {
	if status < int32(EventUpcoming) || status > int32(EventEnded) {
		return errors.BadRequest("EVENT_STATUS_INVALID", "event status must be 0, 1 or 2")
	}
	return nil
}
