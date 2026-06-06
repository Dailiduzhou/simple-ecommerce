package server

import (
	"context"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	orderv1 "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	paymentv1 "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	custommid "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/server/middleware"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"

	"github.com/go-kratos/kratos/v2/log"
	kratosjwt "github.com/go-kratos/kratos/v2/middleware/auth/jwt"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/selector"
	"github.com/go-kratos/kratos/v2/transport/grpc"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

// NewGRPCServer new a gRPC server.
func NewGRPCServer(c *conf.Server, ac *conf.Auth, authUc *biz.AuthUsecase, mall *service.MallService, user *service.UserService, order *service.OrderService, payment *service.PaymentService, logger log.Logger) *grpc.Server {
	jwtMiddleware := kratosjwt.Server(
		func(t *jwtv5.Token) (any, error) {
			return []byte(ac.AccessTokenSecret), nil
		},
		kratosjwt.WithSigningMethod(jwtv5.SigningMethodHS256),
		kratosjwt.WithClaims(func() jwtv5.Claims {
			return &biz.EcommerceClaims{}
		}),
	)

	opts := []grpc.ServerOption{
		grpc.Middleware(
			recovery.Recovery(),
			selector.Server(
				jwtMiddleware,
				custommid.InjectClaims(),
				custommid.CheckBlacklist(authUc),
			).
				Match(grpcWhiteListMatcher()).
				Build(),
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
	paymentv1.RegisterWechatPayServiceServer(srv, payment)
	return srv
}

func grpcWhiteListMatcher() selector.MatchFunc {
	whiteList := map[string]struct{}{
		userv1.OperationUserRegister:     {},
		userv1.OperationUserLogin:        {},
		userv1.OperationUserRefreshToken: {},
	}
	return func(ctx context.Context, operation string) bool {
		_, ok := whiteList[operation]
		return !ok
	}
}
