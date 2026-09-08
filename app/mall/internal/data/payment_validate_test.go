package data

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/go-pay/gopay/wechat"
	"github.com/stretchr/testify/require"
)

func validatePayment() db.Payment {
	return db.Payment{ID: 1, OrderID: 2, AmountMinor: 10000, Currency: "CNY", PayChannel: "wechat:native", OutTradeNo: "pay_1"}
}

func validateResult(state biz.TradeState, amount int64, currency string) *biz.PaymentQueryResult {
	return &biz.PaymentQueryResult{
		Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1",
		TradeState: state, Amount: amount, Currency: currency,
	}
}

func TestValidateProviderResult_AmountOnlyCheckedWhenMoneyMoved(t *testing.T) {
	method, err := biz.ParsePaymentMethod("wechat:native")
	require.NoError(t, err)
	payment := validatePayment()

	tests := []struct {
		name   string
		result *biz.PaymentQueryResult
		want   string
	}{
		{"closed with zero amount passes", validateResult(biz.TradeStateClosed, 0, ""), ""},
		{"pay_error with wrong currency passes", validateResult(biz.TradeStatePayError, 0, "USD"), ""},
		{"success amount mismatch caught", validateResult(biz.TradeStateSuccess, 1, "CNY"), "amount mismatch"},
		{"success currency mismatch caught", validateResult(biz.TradeStateSuccess, 10000, "USD"), "currency mismatch"},
		{"refund amount mismatch caught", validateResult(biz.TradeStateRefund, 1, "CNY"), "amount mismatch"},
		{"success exact match passes", validateResult(biz.TradeStateSuccess, 10000, "CNY"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, validateProviderResult(payment, method, tt.result))
		})
	}

	t.Run("out_trade_no mismatch caught in any state", func(t *testing.T) {
		result := validateResult(biz.TradeStateClosed, 0, "")
		result.OutTradeNo = "other"
		require.Equal(t, "out_trade_no mismatch", validateProviderResult(payment, method, result))
	})
}

func signedWechatXML(t *testing.T, fields gopay.BodyMap) string {
	t.Helper()
	// The helper may be called repeatedly with the same map after a field is
	// mutated. Do not let the previous signature participate in re-signing.
	fields.Remove("sign")
	fields.Set("sign", wechat.GetReleaseSign("api_key_1", wechat.SignType_MD5, fields))
	encoded, err := xml.Marshal(fields)
	require.NoError(t, err)
	return string(encoded)
}

func testWechatAdapter(t *testing.T, fields gopay.BodyMap) *WechatPaymentAdapter {
	t.Helper()
	return testWechatAdapterXML(t, signedWechatXML(t, fields))
}

func testWechatAdapterXML(t *testing.T, responseXML string) *WechatPaymentAdapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(responseXML))
	}))
	t.Cleanup(srv.Close)
	client := wechat.NewClient("app_1", "mch_1", "api_key_1", true)
	client.BaseURL = srv.URL
	return &WechatPaymentAdapter{client: client, apiKey: "api_key_1", notifyURL: "https://merchant.example/notify", log: log.NewHelper(log.DefaultLogger)}
}

func TestWechatQuery_OrderNotExistReturnsSentinel(t *testing.T) {
	adapter := testWechatAdapter(t, gopay.BodyMap{"return_code": "SUCCESS", "result_code": "FAIL", "err_code": "ORDERNOTEXIST", "appid": "app_1", "mch_id": "mch_1"})
	_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{
		Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1",
	})
	require.ErrorIs(t, err, biz.ErrProviderOrderNotExist)
}

func TestWechatQuery_NotPayWithoutTotalFeeSucceeds(t *testing.T) {
	adapter := testWechatAdapter(t, gopay.BodyMap{"return_code": "SUCCESS", "result_code": "SUCCESS", "appid": "app_1", "mch_id": "mch_1", "out_trade_no": "pay_1", "trade_state": "NOTPAY", "trade_state_desc": "not paid yet"})
	result, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{
		Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1",
	})
	require.NoError(t, err)
	require.Equal(t, biz.TradeStateNotPay, result.TradeState)
	require.Zero(t, result.Amount)
}

func TestWechatQuery_SuccessWithoutTotalFeeFails(t *testing.T) {
	adapter := testWechatAdapter(t, gopay.BodyMap{"return_code": "SUCCESS", "result_code": "SUCCESS", "appid": "app_1", "mch_id": "mch_1", "out_trade_no": "pay_1", "transaction_id": "tx_1", "trade_state": "SUCCESS"})
	_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{
		Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1",
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, biz.ErrProviderOrderNotExist)
}

func TestWechatQuery_RejectsInvalidSignatureAndMerchant(t *testing.T) {
	t.Run("invalid signature", func(t *testing.T) {
		fields := gopay.BodyMap{"return_code": "SUCCESS", "result_code": "SUCCESS", "appid": "app_1", "mch_id": "mch_1", "out_trade_no": "pay_1", "trade_state": "NOTPAY"}
		fields.Set("sign", "invalid")
		encoded, marshalErr := xml.Marshal(fields)
		require.NoError(t, marshalErr)
		adapter := testWechatAdapterXML(t, string(encoded))
		_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1"})
		require.ErrorContains(t, err, "signature")
	})

	t.Run("other configured merchant", func(t *testing.T) {
		adapter := testWechatAdapter(t, gopay.BodyMap{"return_code": "SUCCESS", "result_code": "SUCCESS", "appid": "other_app", "mch_id": "mch_1", "out_trade_no": "pay_1", "trade_state": "NOTPAY"})
		_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1"})
		require.ErrorContains(t, err, "identity")
	})
}

func TestWechatPrepay_RejectsProviderBusinessFailure(t *testing.T) {
	adapter := testWechatAdapter(t, gopay.BodyMap{"return_code": "SUCCESS", "result_code": "FAIL", "err_code": "INVALID_REQUEST", "appid": "app_1", "mch_id": "mch_1"})
	_, err := adapter.Prepay(context.Background(), biz.PaymentPrepayRequest{
		Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1",
		Description: "order", Amount: 100, Currency: "CNY", ClientIP: "127.0.0.1",
	})
	require.ErrorContains(t, err, "INVALID_REQUEST")
}

func TestWechatNotification_VerifiesSignatureMerchantAndCurrency(t *testing.T) {
	fields := gopay.BodyMap{
		"return_code": "SUCCESS", "result_code": "SUCCESS", "appid": "app_1", "mch_id": "mch_1",
		"out_trade_no": "pay_1", "transaction_id": "tx_1", "total_fee": "100", "fee_type": "CNY",
	}
	client := wechat.NewClient("app_1", "mch_1", "api_key_1", true)
	adapter := &WechatPaymentAdapter{client: client, apiKey: "api_key_1", log: log.NewHelper(log.DefaultLogger)}
	notify, err := adapter.ParseAndVerifyNotification(httptest.NewRequest("POST", "/", strings.NewReader(signedWechatXML(t, fields))))
	require.NoError(t, err)
	require.Equal(t, int64(100), notify.Amount)

	fields.Set("appid", "other_app")
	_, err = adapter.ParseAndVerifyNotification(httptest.NewRequest("POST", "/", strings.NewReader(signedWechatXML(t, fields))))
	require.ErrorContains(t, err, "merchant identity")
}

func testAlipayAdapter(t *testing.T, status int, responseJSON string) *AlipayPaymentAdapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(responseJSON))
	}))
	t.Cleanup(srv.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	// gopay 期望不带 PEM 头尾的单行 base64 私钥,由其自行补齐 PEM 包装。
	rawKey := base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key))
	client, err := alipayv3.NewClientV3("app_1", rawKey, true)
	require.NoError(t, err)
	client.SetProxyHost(srv.URL)
	return NewAlipayPaymentAdapter(client, log.DefaultLogger)
}

func TestAlipayQuery_TradeNotExistReturnsSentinel(t *testing.T) {
	adapter := testAlipayAdapter(t, http.StatusBadRequest, `{"code":"ACQ.TRADE_NOT_EXIST","message":"trade not exist"}`)
	_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{
		Method: biz.PaymentMethod{Provider: "alipay", Product: "wap"}, OutTradeNo: "pay_1",
	})
	require.ErrorIs(t, err, biz.ErrProviderOrderNotExist)
}

func TestAlipayQuery_OtherBusinessErrorSurfaces(t *testing.T) {
	adapter := testAlipayAdapter(t, http.StatusBadRequest, `{"code":"ACQ.SYSTEM_ERROR","message":"system busy"}`)
	_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{
		Method: biz.PaymentMethod{Provider: "alipay", Product: "wap"}, OutTradeNo: "pay_1",
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, biz.ErrProviderOrderNotExist)
	require.Contains(t, err.Error(), "ACQ.SYSTEM_ERROR")
}

func TestAlipayNotification_BindsConfiguredAppAndSuccessfulTrade(t *testing.T) {
	adapter := &AlipayPaymentAdapter{expectedAppID: "app_1"}
	valid := gopay.BodyMap{
		"app_id": "app_1", "trade_status": "TRADE_SUCCESS", "notify_id": "notify_1",
		"out_trade_no": "pay_1", "trade_no": "trade_1",
	}
	require.NoError(t, adapter.validateAlipayNotification(valid))

	valid.Set("app_id", "other_app")
	require.ErrorContains(t, adapter.validateAlipayNotification(valid), "app_id")
	valid.Set("app_id", "app_1").Set("trade_status", "WAIT_BUYER_PAY")
	require.ErrorContains(t, adapter.validateAlipayNotification(valid), "successful payment")
}
