package biz

import (
	"context"
	"fmt"

	"github.com/go-kratos/kratos/v2/errors"
)

type paymentGateway struct {
	adapters map[string]PaymentAdapter
}

func NewPaymentGateway(adapters []PaymentAdapter) PaymentGateway {
	gateway := &paymentGateway{adapters: make(map[string]PaymentAdapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		channel := NormalizePayChannel(adapter.Channel())
		if channel == "" {
			continue
		}
		gateway.adapters[channel] = adapter
	}
	return gateway
}

func (g *paymentGateway) Prepay(ctx context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error) {
	adapter, err := g.adapter(req.Channel)
	if err != nil {
		return nil, err
	}
	req.Channel = NormalizePayChannel(req.Channel)
	return adapter.Prepay(ctx, req)
}

func (g *paymentGateway) QueryOrder(ctx context.Context, req PaymentQueryRequest) (*PaymentQueryResult, error) {
	adapter, err := g.adapter(req.Channel)
	if err != nil {
		return nil, err
	}
	req.Channel = NormalizePayChannel(req.Channel)
	return adapter.QueryOrder(ctx, req)
}

func (g *paymentGateway) CloseOrder(ctx context.Context, req PaymentCloseRequest) (*PaymentCloseResult, error) {
	adapter, err := g.adapter(req.Channel)
	if err != nil {
		return nil, err
	}
	req.Channel = NormalizePayChannel(req.Channel)
	return adapter.CloseOrder(ctx, req)
}

func (g *paymentGateway) adapter(channel string) (PaymentAdapter, error) {
	normalized := NormalizePayChannel(channel)
	if normalized == "" {
		return nil, errors.BadRequest("PAY_CHANNEL_REQUIRED", "pay channel is required")
	}
	adapter, ok := g.adapters[normalized]
	if !ok {
		return nil, errors.ServiceUnavailable("PAY_CHANNEL_NOT_SUPPORTED", fmt.Sprintf("pay channel %q is not supported", normalized))
	}
	return adapter, nil
}
