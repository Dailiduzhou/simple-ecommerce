package server

import (
	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	orderv1 "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	paymentv1 "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/transport/http"
)

// NewHTTPServer new an HTTP server.
func NewHTTPServer(c *conf.Server, mall *service.MallService, user *service.UserService, order *service.OrderService, payment *service.PaymentService, logger log.Logger) *http.Server {
	opts := []http.ServerOption{
		http.Middleware(
			recovery.Recovery(),
		),
	}
	if c.Http.Network != "" {
		opts = append(opts, http.Network(c.Http.Network))
	}
	if c.Http.Addr != "" {
		opts = append(opts, http.Address(c.Http.Addr))
	}
	if c.Http.Timeout != nil {
		opts = append(opts, http.Timeout(c.Http.Timeout.AsDuration()))
	}
	srv := http.NewServer(opts...)
	mallv1.RegisterMallHTTPServer(srv, mall)
	userv1.RegisterUserHTTPServer(srv, user)
	orderv1.RegisterOrderHTTPServer(srv, order)
	paymentv1.RegisterPaymentHTTPServer(srv, payment)
	return srv
}
