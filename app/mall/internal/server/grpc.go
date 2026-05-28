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
	"github.com/go-kratos/kratos/v2/transport/grpc"
)

// NewGRPCServer new a gRPC server.
func NewGRPCServer(c *conf.Server, mall *service.MallService, user *service.UserService, order *service.OrderService, payment *service.PaymentService, logger log.Logger) *grpc.Server {
	opts := []grpc.ServerOption{
		grpc.Middleware(
			recovery.Recovery(),
		),
	}
	if c.Grpc.Network != "" {
		opts = append(opts, grpc.Network(c.Grpc.Network))
	}
	if c.Grpc.Addr != "" {
		opts = append(opts, grpc.Address(c.Grpc.Addr))
	}
	if c.Grpc.Timeout != nil {
		opts = append(opts, grpc.Timeout(c.Grpc.Timeout.AsDuration()))
	}
	srv := grpc.NewServer(opts...)
	mallv1.RegisterMallServer(srv, mall)
	userv1.RegisterUserServer(srv, user)
	orderv1.RegisterOrderServer(srv, order)
	paymentv1.RegisterPaymentServer(srv, payment)
	return srv
}
