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
	"github.com/go-kratos/kratos/v2/transport/http"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const (
	OperationUserRegister     = userv1.OperationUserRegister
	OperationUserLogin        = userv1.OperationUserLogin
	OperationUserRefreshToken = userv1.OperationUserRefreshToken
)

// NewHTTPServer new an HTTP server.
func NewHTTPServer(c *conf.Server, ac *conf.Auth, authUc *biz.AuthUsecase, mall *service.MallService, user *service.UserService, order *service.OrderService, payment *service.PaymentService, logger log.Logger) *http.Server {
	jwtMiddleware := kratosjwt.Server(
		func(t *jwtv5.Token) (any, error) {
			return []byte(ac.AccessTokenSecret), nil
		},
		kratosjwt.WithSigningMethod(jwtv5.SigningMethodHS256),
		kratosjwt.WithClaims(func() jwtv5.Claims {
			return &biz.EcommerceClaims{}
		}),
	)

	opts := []http.ServerOption{
		http.Middleware(
			recovery.Recovery(),
			selector.Server(
				jwtMiddleware,
				custommid.InjectClaims(),
				custommid.CheckBlacklist(authUc),
			).
				Match(newWhiteListMatcher()).
				Build(),
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
	paymentv1.RegisterWechatPayServiceHTTPServer(srv, payment)
	srv.Route("/").POST("/v1/pay/wechat/notify", payment.HandleWechatPayNotify)
	return srv
}

func newWhiteListMatcher() selector.MatchFunc {
	whiteList := map[string]struct{}{
		OperationUserRegister:     {},
		OperationUserLogin:        {},
		OperationUserRefreshToken: {},
	}
	return func(ctx context.Context, operation string) bool {
		_, ok := whiteList[operation]
		return !ok // false = skip auth chain (whitelisted)
	}
}
