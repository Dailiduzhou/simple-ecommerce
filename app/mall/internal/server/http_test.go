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

func newHTTPTestServer(uc biz.PaymentUsecase) http.Handler {
	mall := service.NewMallService(nil, nil, nil, log.DefaultLogger)
	user := service.NewUserService(nil, nil, nil, log.DefaultLogger)
	order := service.NewOrderService(nil)
	payment := service.NewPaymentService(uc, nil, log.DefaultLogger)
	return NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}, nil, mall, user, order, payment, log.DefaultLogger)
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

func TestAnonymousBusinessPaymentAPIStillRequiresJWT(t *testing.T) {
	server := newHTTPTestServer(&callbackPaymentUsecase{})
	request := httptest.NewRequest(http.MethodGet, "/v1/payments/1", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
}
