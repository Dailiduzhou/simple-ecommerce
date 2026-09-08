package biz

import (
	"context"
	"net/http"
	"strings"
)

type paymentGateway struct{ adapters map[string]PaymentAdapter }

func NewPaymentGateway(adapters []PaymentAdapter) PaymentGateway {
	gateway := &paymentGateway{adapters: make(map[string]PaymentAdapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(adapter.Provider()))
		if provider != "" {
			gateway.adapters[provider] = adapter
		}
	}
	return gateway
}

func (g *paymentGateway) adapter(method PaymentMethod) (PaymentAdapter, error) {
	method = method.Normalize()
	adapter, ok := g.adapters[method.Provider]
	if !ok || !adapter.Supports(method) {
		return nil, ErrPaymentProviderUnavailable
	}
	return adapter, nil
}

func (g *paymentGateway) Capabilities(method PaymentMethod) (PaymentCapabilities, error) {
	adapter, err := g.adapter(method)
	if err != nil {
		return PaymentCapabilities{}, err
	}
	return adapter.Capabilities(method.Normalize()), nil
}

func (g *paymentGateway) Prepay(ctx context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error) {
	adapter, err := g.adapter(req.Method)
	if err != nil {
		return nil, err
	}
	req.Method = req.Method.Normalize()
	return adapter.Prepay(ctx, req)
}

func (g *paymentGateway) Query(ctx context.Context, req PaymentQueryRequest) (*PaymentQueryResult, error) {
	adapter, err := g.adapter(req.Method)
	if err != nil {
		return nil, err
	}
	req.Method = req.Method.Normalize()
	return adapter.Query(ctx, req)
}

func (g *paymentGateway) Close(ctx context.Context, req PaymentCloseRequest) (*PaymentCloseResult, error) {
	adapter, err := g.adapter(req.Method)
	if err != nil {
		return nil, err
	}
	req.Method = req.Method.Normalize()
	return adapter.Close(ctx, req)
}

func (g *paymentGateway) Refund(ctx context.Context, req PaymentRefundRequest) (*PaymentRefundResult, error) {
	adapter, err := g.adapter(req.Method)
	if err != nil {
		return nil, err
	}
	req.Method = req.Method.Normalize()
	return adapter.Refund(ctx, req)
}

func (g *paymentGateway) ParseAndVerifyNotification(provider string, request *http.Request) (*PaymentNotification, error) {
	adapter, ok := g.adapters[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return nil, ErrPaymentProviderUnavailable
	}
	return adapter.ParseAndVerifyNotification(request)
}

func (g *paymentGateway) NotificationAck(provider string, success bool) (PaymentNotificationAck, error) {
	adapter, ok := g.adapters[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return PaymentNotificationAck{}, ErrPaymentProviderUnavailable
	}
	return adapter.NotificationAck(success), nil
}
