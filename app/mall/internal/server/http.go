package server

import (
	"context"
	"strings"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	orderv1 "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
	paymentv1 "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	custommid "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/server/middleware"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"

	validatorv2 "github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/log"
	kratosjwt "github.com/go-kratos/kratos/v2/middleware/auth/jwt"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/selector"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"github.com/go-kratos/kratos/v2/transport/http"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const (
	OperationUserRegister     = userv1.OperationUserRegister
	OperationUserLogin        = userv1.OperationUserLogin
	OperationUserRefreshToken = userv1.OperationUserRefreshToken
)

// NewHTTPServer new an HTTP server.
func NewHTTPServer(c *conf.Server, ac *conf.Auth, authUc biz.AuthUsecase, mall *service.MallService, user *service.UserService, order *service.OrderService, payment *service.PaymentService, logger log.Logger) *http.Server {
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
			tracing.Server(),
			custommid.SafeLogging(logger),
			validatorv2.ProtoValidate(),
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
	// Payment service and callback routing are provider-neutral.
	paymentv1.RegisterPaymentHTTPServer(srv, payment)
	srv.Route("/").POST("/v1/payments/{provider}/notify", payment.HandlePaymentNotify)
	return srv
}

func newWhiteListMatcher() selector.MatchFunc {
	whiteList := map[string]struct{}{
		OperationUserRegister:     {},
		OperationUserLogin:        {},
		OperationUserRefreshToken: {},
	}
	return func(ctx context.Context, operation string) bool {
		if request, ok := http.RequestFromServerContext(ctx); ok && request.Method == "POST" {
			path := strings.TrimSuffix(request.URL.Path, "/")
			if isPaymentCallbackPath(path) {
				return false
			}
		}
		_, ok := whiteList[operation]
		return !ok // false = skip auth chain (whitelisted)
	}
}

func isPaymentCallbackPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 5 && parts[1] == "v1" && parts[2] == "payments" && parts[3] != "" && parts[4] == "notify"
}
