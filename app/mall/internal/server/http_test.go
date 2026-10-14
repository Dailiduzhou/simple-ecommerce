package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type callbackPaymentUsecase struct {
	provider    string
	callbackErr error
}

func (u *callbackPaymentUsecase) PrepayForOrder(context.Context, biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) GetPayment(context.Context, int64, int64) (*biz.PaymentDO, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) GetPaymentByOrder(context.Context, int64, int64) (*biz.PaymentDO, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) QueryPayment(context.Context, string, int64) (*biz.PaymentQueryResult, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) ClosePayment(context.Context, string, int64) (*biz.PaymentCloseResult, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) RefundPayment(context.Context, int64) (*biz.PaymentRefundResult, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) ReconcilePendingRefunds(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}
func (u *callbackPaymentUsecase) CreateCheckJob(context.Context, int64, int, time.Duration, time.Duration, string) (*biz.MQJob, error) {
	return nil, nil
}
func (u *callbackPaymentUsecase) HandleNotification(_ context.Context, provider string, _ *http.Request) error {
	u.provider = provider
	return u.callbackErr
}
func (u *callbackPaymentUsecase) NotificationAck(provider string, success bool) biz.PaymentNotificationAck {
	if provider == "alipay" {
		body := "fail"
		if success {
			body = "success"
		}
		return biz.PaymentNotificationAck{StatusCode: http.StatusOK, ContentType: "text/plain", Body: []byte(body)}
	}
	return biz.DefaultPaymentNotificationAck()
}
func (u *callbackPaymentUsecase) SupportsNotificationProvider(provider string) bool {
	return provider == "wechat" || provider == "alipay"
}

func newHTTPTestServer(uc biz.PaymentUsecase) http.Handler {
	return newHTTPTestServerWithConfig(uc, &conf.Server{Http: &conf.Server_HTTP{}})
}

func newHTTPTestServerWithConfig(uc biz.PaymentUsecase, c *conf.Server) http.Handler {
	mall := service.NewMallService(nil, nil, nil, nil, log.DefaultLogger)
	user := service.NewUserService(nil, nil, nil, nil, log.DefaultLogger)
	order := service.NewOrderService(nil, nil)
	payment := service.NewPaymentService(uc, nil, nil, log.DefaultLogger)
	return NewHTTPServer(c, &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}, nil, mall, user, order, payment, service.NewCommunityService(nil, nil), service.NewMediaService(nil), &communityLimiter{}, log.DefaultLogger)
}

func TestPaymentCallbackIsPublicAndUsesProviderAck(t *testing.T) {
	uc := &callbackPaymentUsecase{}
	server := newHTTPTestServer(uc)
	request := httptest.NewRequest(http.MethodPost, "/v1/payments/alipay/notify", strings.NewReader("signed=payload"))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "success", response.Body.String())
	require.Equal(t, "alipay", uc.provider)
}

func TestPaymentCallbackPersistenceFailureReturnsProviderFailureAck(t *testing.T) {
	server := newHTTPTestServer(&callbackPaymentUsecase{callbackErr: fmt.Errorf("database unavailable")})
	request := httptest.NewRequest(http.MethodPost, "/v1/payments/alipay/notify", strings.NewReader("signed=payload"))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, "fail", response.Body.String())
}

func TestUnsupportedPaymentCallbackProviderIsRejectedBeforeUsecase(t *testing.T) {
	uc := &callbackPaymentUsecase{}
	server := newHTTPTestServer(uc)
	request := httptest.NewRequest(http.MethodPost, "/v1/payments/random/notify", strings.NewReader("payload"))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Empty(t, uc.provider)
}

func TestAnonymousBusinessPaymentAPIStillRequiresJWT(t *testing.T) {
	server := newHTTPTestServer(&callbackPaymentUsecase{})
	request := httptest.NewRequest(http.MethodGet, "/v1/payments/1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestAuthenticationPrecedesProtoValidation(t *testing.T) {
	server := newHTTPTestServer(&callbackPaymentUsecase{})
	request := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestPathCodecErrorIsSanitized(t *testing.T) {
	server := newHTTPTestServer(&callbackPaymentUsecase{})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/users/register", nil))
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "INVALID_ARGUMENT")
	require.NotContains(t, response.Body.String(), "strconv")
	require.NotContains(t, response.Body.String(), "parsing field")
}

func TestProductionCORSAllowlist(t *testing.T) {
	production := true
	server := newHTTPTestServerWithConfig(&callbackPaymentUsecase{}, &conf.Server{IsProduction: &production, Http: &conf.Server_HTTP{CorsAllowedOrigins: "https://shop.example"}})
	for _, tc := range []struct {
		origin, method, headers string
		code                    int
	}{
		{"https://shop.example", "POST", "Authorization, Content-Type", 204},
		{"http://evil.example", "GET", "", 403},
		{"https://shop.example.evil.example", "GET", "", 403},
		{"null", "GET", "", 403},
		{"https://shop.example", "TRACE", "", 403},
		{"https://shop.example", "POST", "X-Arbitrary-Header", 403},
	} {
		req := httptest.NewRequest(http.MethodOptions, "/v1/orders", nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Access-Control-Request-Method", tc.method)
		req.Header.Set("Access-Control-Request-Headers", tc.headers)
		res := httptest.NewRecorder()
		server.ServeHTTP(res, req)
		require.Equal(t, tc.code, res.Code)
		require.Empty(t, res.Header().Get("Access-Control-Allow-Credentials"))
		if tc.code == 204 {
			require.Equal(t, tc.origin, res.Header().Get("Access-Control-Allow-Origin"))
		} else {
			require.Empty(t, res.Header().Get("Access-Control-Allow-Origin"))
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/1", nil)
	req.Header.Set("Origin", "https://shop.example")
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	require.Equal(t, http.StatusUnauthorized, res.Code)
	require.Equal(t, "https://shop.example", res.Header().Get("Access-Control-Allow-Origin"))
}
