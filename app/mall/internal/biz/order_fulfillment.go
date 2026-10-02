package biz

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
)

const (
	OrderActionShip     = "ship"
	OrderActionComplete = "complete"
)

var ErrOrderFulfillmentConflict = errors.Conflict("ORDER_FULFILLMENT_CONFLICT", "order cannot perform this fulfillment transition")

type OrderFulfillmentInput struct {
	OrderID                                                 int64
	Action, IdempotencyKey, Reason, Carrier, TrackingNumber string
}

func (i OrderFulfillmentInput) Validate(a Actor) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if i.Action == OrderActionShip && !a.Admin {
		return errors.Forbidden("FORBIDDEN", "administrator role is required to ship an order")
	}
	if i.OrderID <= 0 || (i.Action != OrderActionShip && i.Action != OrderActionComplete) ||
		len(i.IdempotencyKey) < 8 || len(i.IdempotencyKey) > 64 || strings.TrimSpace(i.IdempotencyKey) != i.IdempotencyKey ||
		strings.TrimSpace(i.Reason) == "" || utf8.RuneCountInString(i.Reason) > 255 {
		return errors.BadRequest("ORDER_FULFILLMENT_INVALID", "positive order ID, action, reason and 8-64 byte idempotency key are required")
	}
	if i.Action == OrderActionShip {
		if strings.TrimSpace(i.Carrier) == "" || utf8.RuneCountInString(i.Carrier) > 64 || strings.TrimSpace(i.TrackingNumber) == "" || utf8.RuneCountInString(i.TrackingNumber) > 128 {
			return errors.BadRequest("ORDER_SHIPMENT_INVALID", "carrier and tracking number are required")
		}
	} else if i.Carrier != "" || i.TrackingNumber != "" {
		return errors.BadRequest("ORDER_FULFILLMENT_INVALID", "completion cannot change shipment information")
	}
	return nil
}

type OrderFulfillmentAction struct {
	ID, OrderID, ActorID                                                          int64
	Action, FromStatus, ToStatus, IdempotencyKey, Reason, Carrier, TrackingNumber string
	CreatedAt                                                                     time.Time
}

type OrderFulfillmentRepo interface {
	ApplyFulfillment(context.Context, Actor, OrderFulfillmentInput) (*OrderFulfillmentAction, error)
	ListFulfillmentActions(context.Context, Actor, int64) ([]OrderFulfillmentAction, error)
}

type OrderFulfillmentUsecase struct{ repo OrderFulfillmentRepo }

func NewOrderFulfillmentUsecase(repo OrderFulfillmentRepo) *OrderFulfillmentUsecase {
	return &OrderFulfillmentUsecase{repo: repo}
}

func (uc *OrderFulfillmentUsecase) Apply(ctx context.Context, a Actor, i OrderFulfillmentInput) (*OrderFulfillmentAction, error) {
	if err := i.Validate(a); err != nil {
		return nil, err
	}
	return uc.repo.ApplyFulfillment(ctx, a, i)
}

func (uc *OrderFulfillmentUsecase) List(ctx context.Context, a Actor, orderID int64) ([]OrderFulfillmentAction, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if orderID <= 0 {
		return nil, ErrOrderNotFound
	}
	return uc.repo.ListFulfillmentActions(ctx, a, orderID)
}
