package biz

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrderFulfillmentInputBoundaries(t *testing.T) {
	valid := OrderFulfillmentInput{OrderID: 7, Action: OrderActionShip, IdempotencyKey: "shipment-1", Reason: "sent", Carrier: "carrier", TrackingNumber: "tracking"}
	a := Actor{ID: 1, Admin: true}
	require.NoError(t, valid.Validate(a))
	for name, mutate := range map[string]func(*OrderFulfillmentInput){
		"order":                      func(i *OrderFulfillmentInput) { i.OrderID = 0 },
		"action":                     func(i *OrderFulfillmentInput) { i.Action = "refund" },
		"blank reason":               func(i *OrderFulfillmentInput) { i.Reason = "  " },
		"long reason":                func(i *OrderFulfillmentInput) { i.Reason = strings.Repeat("字", 256) },
		"short key":                  func(i *OrderFulfillmentInput) { i.IdempotencyKey = "short" },
		"long key":                   func(i *OrderFulfillmentInput) { i.IdempotencyKey = strings.Repeat("x", 65) },
		"spaced key":                 func(i *OrderFulfillmentInput) { i.IdempotencyKey = " shipment-1" },
		"missing carrier":            func(i *OrderFulfillmentInput) { i.Carrier = " " },
		"long carrier":               func(i *OrderFulfillmentInput) { i.Carrier = strings.Repeat("x", 65) },
		"missing tracking":           func(i *OrderFulfillmentInput) { i.TrackingNumber = "" },
		"long tracking":              func(i *OrderFulfillmentInput) { i.TrackingNumber = strings.Repeat("x", 129) },
		"complete rewrites shipment": func(i *OrderFulfillmentInput) { i.Action = OrderActionComplete },
	} {
		t.Run(name, func(t *testing.T) { i := valid; mutate(&i); require.Error(t, i.Validate(a)) })
	}
	require.Error(t, valid.Validate(Actor{}))
	require.Error(t, valid.Validate(Actor{ID: 1}))
	valid.Action, valid.Carrier, valid.TrackingNumber = OrderActionComplete, "", ""
	require.NoError(t, valid.Validate(Actor{ID: 1}))
}
