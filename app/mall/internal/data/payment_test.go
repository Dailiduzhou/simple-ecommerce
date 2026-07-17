package data

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestNewPaymentAdapters_AllProvidersOptional(t *testing.T) {
	adapters, err := NewPaymentAdapters(nil, log.DefaultLogger)
	require.NoError(t, err)
	require.Empty(t, adapters)

	adapters, err = NewPaymentAdapters(&conf.Payment{Wechat: &conf.Wechat{}, Alipay: &conf.Alipay{}}, log.DefaultLogger)
	require.NoError(t, err)
	require.Empty(t, adapters)
}

func TestPaymentNotificationVerificationFailsClosedWithoutConfiguration(t *testing.T) {
	wechat := NewWechatPaymentAdapter(nil, log.DefaultLogger)
	_, err := wechat.ParseAndVerifyNotification(httptest.NewRequest("POST", "/", strings.NewReader("<xml/>")))
	require.Error(t, err)
	alipay := NewAlipayPaymentAdapter(nil, log.DefaultLogger)
	_, err = alipay.ParseAndVerifyNotification(httptest.NewRequest("POST", "/", strings.NewReader("a=b")))
	require.Error(t, err)
}

func TestNewPaymentAdapters_EnabledIncompleteProviderFailsStartup(t *testing.T) {
	_, err := NewPaymentAdapters(&conf.Payment{Wechat: &conf.Wechat{Enabled: true, AppId: "app"}}, log.DefaultLogger)
	require.ErrorContains(t, err, "enabled")
}

func TestPaymentActionIsProviderNeutralJSON(t *testing.T) {
	action := paymentAction(biz.PaymentActionInvoke, map[string]string{"sdk": "opaque"})
	require.Equal(t, biz.PaymentActionInvoke, action.Type)
	require.JSONEq(t, `{"sdk":"opaque"}`, string(action.Payload))
}

func TestPaymentAdaptersExposeCapabilitiesByMethod(t *testing.T) {
	wechat := NewWechatPaymentAdapter(nil, log.DefaultLogger)
	require.True(t, wechat.Supports(biz.PaymentMethod{Provider: "wechat", Product: "native"}))
	require.False(t, wechat.Supports(biz.PaymentMethod{Provider: "alipay", Product: "wap"}))
	require.True(t, wechat.Capabilities(biz.PaymentMethod{}).RequiresPoll)

	alipay := NewAlipayPaymentAdapter(nil, log.DefaultLogger)
	require.True(t, alipay.Supports(biz.PaymentMethod{Provider: "alipay", Product: "app"}))
	require.False(t, alipay.Capabilities(biz.PaymentMethod{}).RequiresPoll)
}

func TestPaymentAdaptersOwnProviderAcknowledgements(t *testing.T) {
	wechat := NewWechatPaymentAdapter(nil, log.DefaultLogger)
	require.Contains(t, string(wechat.NotificationAck(true).Body), "SUCCESS")
	require.Contains(t, string(wechat.NotificationAck(false).Body), "FAIL")

	alipay := NewAlipayPaymentAdapter(nil, log.DefaultLogger)
	require.Equal(t, "success", string(alipay.NotificationAck(true).Body))
	require.Equal(t, "fail", string(alipay.NotificationAck(false).Body))
}
