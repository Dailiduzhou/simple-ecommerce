package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/go-pay/gopay/wechat"
	"github.com/go-pay/util"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/shopspring/decimal"
)

const (
	EnvPaymentCallbackBaseURL = "PAYMENT_CALLBACK_BASE_URL"
	EnvAlipayNotifyURL        = "ALIPAY_NOTIFY_URL"
	EnvWechatNotifyURL        = "WECHAT_NOTIFY_URL"
	maxNotificationBody       = 1 << 20
)

func notifyURLFromEnv(provider string) string {
	if base := strings.TrimRight(os.Getenv(EnvPaymentCallbackBaseURL), "/"); base != "" {
		return base + "/v1/payments/" + provider + "/notify"
	}
	if provider == "wechat" {
		return os.Getenv(EnvWechatNotifyURL)
	}
	return os.Getenv(EnvAlipayNotifyURL)
}

var (
	_ biz.PaymentAdapter          = (*WechatPaymentAdapter)(nil)
	_ biz.PaymentAdapter          = (*AlipayPaymentAdapter)(nil)
	_ biz.PaymentRepo             = (*PaymentRepo)(nil)
	_ biz.PaymentMQRepo           = (*PaymentMQRepo)(nil)
	_ biz.PaymentNotificationRepo = (*PaymentNotificationRepo)(nil)
)

type WechatPaymentAdapter struct {
	client            *wechat.Client
	apiKey, notifyURL string
	log               *log.Helper
}

func NewWechatPaymentAdapter(c *conf.Payment, logger log.Logger) *WechatPaymentAdapter {
	adapter := &WechatPaymentAdapter{notifyURL: notifyURLFromEnv("wechat"), log: log.NewHelper(logger)}
	if c != nil && c.Wechat != nil && c.Wechat.AppId != "" && c.Wechat.MchId != "" && c.Wechat.ApiKey != "" {
		adapter.apiKey = c.Wechat.ApiKey
		adapter.client = wechat.NewClient(c.Wechat.AppId, c.Wechat.MchId, c.Wechat.ApiKey, c.Wechat.IsProduction)
	}
	return adapter
}

func (a *WechatPaymentAdapter) Provider() string { return "wechat" }
func (a *WechatPaymentAdapter) NotificationAck(success bool) biz.PaymentNotificationAck {
	body := "<xml><return_code><![CDATA[FAIL]]></return_code><return_msg><![CDATA[RETRY]]></return_msg></xml>"
	if success {
		body = "<xml><return_code><![CDATA[SUCCESS]]></return_code><return_msg><![CDATA[OK]]></return_msg></xml>"
	}
	return biz.PaymentNotificationAck{StatusCode: http.StatusOK, ContentType: "application/xml; charset=utf-8", Body: []byte(body)}
}
func (a *WechatPaymentAdapter) Supports(method biz.PaymentMethod) bool {
	method = method.Normalize()
	return method.Provider == a.Provider() && (method.Product == "jsapi" || method.Product == "native" || method.Product == "app")
}
func (a *WechatPaymentAdapter) Capabilities(biz.PaymentMethod) biz.PaymentCapabilities {
	return biz.PaymentCapabilities{SupportsNotify: true, RequiresPoll: true, SupportsClose: true}
}

func (a *WechatPaymentAdapter) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("wechat")
	}
	if err := validateCNYAmount(req.Amount, req.Currency); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tradeType := ""
	switch req.Method.Product {
	case "jsapi":
		tradeType = wechat.TradeType_JsApi
	case "native":
		tradeType = wechat.TradeType_Native
	case "app":
		tradeType = wechat.TradeType_App
	default:
		return nil, biz.ErrPaymentProviderUnavailable
	}
	nonce := util.RandomString(32)
	signType := wechat.SignType_MD5
	body := make(gopay.BodyMap)
	body.Set("nonce_str", nonce).Set("body", req.Description).Set("out_trade_no", req.OutTradeNo).
		Set("total_fee", req.Amount).Set("spbill_create_ip", req.ClientIP).Set("trade_type", tradeType).Set("sign_type", signType)
	if req.Method.Product == "jsapi" {
		body.Set("openid", req.Extension["openid"])
	}
	if a.notifyURL != "" {
		body.Set("notify_url", a.notifyURL)
	}
	response, err := a.client.UnifiedOrder(ctx, body)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("wechat unified order returned empty response")
	}
	if err := a.validateWechatResponse(response, response.ReturnCode, response.ResultCode, response.ErrCode, response.Appid, response.MchId); err != nil {
		return nil, err
	}
	if response.PrepayId == "" {
		return nil, fmt.Errorf("wechat unified order returned empty prepay_id")
	}
	if req.Method.Product == "native" && response.CodeUrl == "" {
		return nil, fmt.Errorf("wechat native unified order returned empty code_url")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	var action biz.PaymentAction
	switch req.Method.Product {
	case "native":
		action = paymentAction(biz.PaymentActionRedirect, map[string]string{"url": response.CodeUrl})
	case "app":
		action = paymentAction(biz.PaymentActionInvoke, map[string]string{
			"appId": a.client.AppId, "partnerId": a.client.MchId, "prepayId": response.PrepayId,
			"nonceStr": response.NonceStr, "timeStamp": timestamp,
			"sign": wechat.GetAppPaySign(a.client.AppId, a.client.MchId, response.NonceStr, response.PrepayId, signType, timestamp, a.client.ApiKey),
		})
	default:
		pkg := "prepay_id=" + response.PrepayId
		action = paymentAction(biz.PaymentActionInvoke, map[string]string{
			"appId": a.client.AppId, "timeStamp": timestamp, "nonceStr": response.NonceStr,
			"package": pkg, "signType": signType,
			"paySign": wechat.GetJsapiPaySign(a.client.AppId, response.NonceStr, pkg, signType, timestamp, a.client.ApiKey),
		})
	}
	return &biz.PaymentPrepayResult{ProviderReference: response.PrepayId, Action: action}, nil
}

func (a *WechatPaymentAdapter) Query(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("wechat")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := make(gopay.BodyMap)
	body.Set("nonce_str", util.RandomString(32)).Set("sign_type", wechat.SignType_MD5)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("transaction_id", req.TransactionID)
	}
	response, responseBody, err := a.client.QueryOrder(ctx, body)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("wechat query returned empty response")
	}
	if err := a.validateWechatResponse(responseBody, response.ReturnCode, response.ResultCode, response.ErrCode, response.Appid, response.MchId); err != nil {
		if response.ErrCode == "ORDERNOTEXIST" {
			return nil, biz.ErrProviderOrderNotExist
		}
		return nil, err
	}
	state := biz.ParseTradeState(response.TradeState)
	var amount int64
	if response.TotalFee != "" {
		amount, err = strconv.ParseInt(response.TotalFee, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse wechat total fee: %w", err)
		}
	} else if state == biz.TradeStateSuccess || state == biz.TradeStateRefund {
		// Money moved but no amount reported: never fabricate a zero amount,
		// it would trip the amount-mismatch reconciliation path downstream.
		return nil, fmt.Errorf("wechat query missing total_fee for state %s", response.TradeState)
	}
	return &biz.PaymentQueryResult{Method: req.Method, OutTradeNo: response.OutTradeNo, TransactionID: response.TransactionId,
		TradeState: state, TradeStateDesc: response.TradeStateDesc,
		RawTradeState: response.TradeState, Amount: amount, Currency: biz.DefaultCurrency}, nil
}

func (a *WechatPaymentAdapter) Close(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("wechat")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := make(gopay.BodyMap)
	body.Set("nonce_str", util.RandomString(32)).Set("out_trade_no", req.OutTradeNo)
	response, err := a.client.CloseOrder(ctx, body)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("wechat close returned empty response")
	}
	if err := a.validateWechatResponse(response, response.ReturnCode, response.ResultCode, response.ErrCode, response.Appid, response.MchId); err != nil {
		return &biz.PaymentCloseResult{Method: req.Method, OutTradeNo: req.OutTradeNo, RawCode: response.ErrCode}, err
	}
	return &biz.PaymentCloseResult{Method: req.Method, OutTradeNo: req.OutTradeNo, Success: true}, nil
}

func (a *WechatPaymentAdapter) Refund(context.Context, biz.PaymentRefundRequest) (*biz.PaymentRefundResult, error) {
	return nil, biz.ErrPaymentProviderUnavailable
}

func (a *WechatPaymentAdapter) ParseAndVerifyNotification(request *http.Request) (*biz.PaymentNotification, error) {
	if a.apiKey == "" || a.client == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_SIGNATURE_CONFIGURATION_MISSING", "wechat signature configuration is missing")
	}
	body, err := boundedRequestBody(request)
	if err != nil {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_BODY_INVALID", err.Error())
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	notify, err := wechat.ParseNotify(request)
	if err != nil {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_PARSE_FAILED", err.Error())
	}
	signType := notify.SignType
	if signType == "" {
		signType = wechat.SignType_MD5
	}
	if signType != wechat.SignType_MD5 && signType != wechat.SignType_HMAC_SHA256 {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_SIGNATURE_INVALID", "wechat notification signature type is not supported")
	}
	valid, err := wechat.VerifySign(a.apiKey, signType, notify)
	if err != nil || !valid {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_SIGNATURE_INVALID", "wechat notification signature is invalid")
	}
	if notify.ReturnCode != "SUCCESS" || notify.ResultCode != "SUCCESS" {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_NOT_SUCCESSFUL", "wechat notification is not a successful payment")
	}
	if notify.Appid != a.client.AppId || notify.MchId != a.client.MchId {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_MERCHANT_MISMATCH", "wechat notification merchant identity does not match configuration")
	}
	if notify.OutTradeNo == "" || notify.TransactionId == "" {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_IDENTITY_INVALID", "wechat notification trade identity is incomplete")
	}
	currency := strings.ToUpper(strings.TrimSpace(notify.FeeType))
	if currency == "" {
		currency = biz.DefaultCurrency
	}
	if currency != biz.DefaultCurrency {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_CURRENCY_INVALID", "wechat notification currency is not supported")
	}
	amount, err := strconv.ParseInt(notify.TotalFee, 10, 64)
	if err != nil || amount <= 0 {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_AMOUNT_INVALID", "wechat total_fee is invalid")
	}
	return &biz.PaymentNotification{Provider: a.Provider(), ProviderEventID: notify.TransactionId, OutTradeNo: notify.OutTradeNo,
		TransactionID: notify.TransactionId, Amount: amount, Currency: currency,
		PayloadHash: sha256Hex(body), VerifiedAt: time.Now().UTC()}, nil
}

func (a *WechatPaymentAdapter) validateWechatResponse(signed any, returnCode, resultCode, errCode, appID, merchantID string) error {
	if returnCode != "SUCCESS" {
		return fmt.Errorf("wechat transport rejected request: %s", returnCode)
	}
	valid, err := wechat.VerifySign(a.apiKey, wechat.SignType_MD5, signed)
	if err != nil || !valid {
		return fmt.Errorf("wechat response signature is invalid")
	}
	if appID != a.client.AppId || merchantID != a.client.MchId {
		return fmt.Errorf("wechat response merchant identity mismatch")
	}
	if resultCode != "SUCCESS" {
		return fmt.Errorf("wechat business request failed: %s", errCode)
	}
	return nil
}

type AlipayPaymentAdapter struct {
	client                    *alipayv3.ClientV3
	tradeRequester            alipayTradeRequester
	notifyURL, publicCertPath string
	expectedAppID             string
	log                       *log.Helper
}

func NewAlipayPaymentAdapter(client *alipayv3.ClientV3, logger log.Logger) *AlipayPaymentAdapter {
	adapter := &AlipayPaymentAdapter{client: client, tradeRequester: client, notifyURL: notifyURLFromEnv("alipay"), log: log.NewHelper(logger)}
	if client != nil {
		adapter.expectedAppID = client.AppId
	}
	return adapter
}
func newAlipayPaymentAdapterForTest(client *alipayv3.ClientV3, requester alipayTradeRequester, logger log.Logger) *AlipayPaymentAdapter {
	adapter := &AlipayPaymentAdapter{client: client, tradeRequester: requester, notifyURL: notifyURLFromEnv("alipay"), log: log.NewHelper(logger)}
	if client != nil {
		adapter.expectedAppID = client.AppId
	}
	return adapter
}
func (a *AlipayPaymentAdapter) Provider() string { return "alipay" }
func (a *AlipayPaymentAdapter) NotificationAck(success bool) biz.PaymentNotificationAck {
	body := "fail"
	if success {
		body = "success"
	}
	return biz.PaymentNotificationAck{StatusCode: http.StatusOK, ContentType: "text/plain; charset=utf-8", Body: []byte(body)}
}
func (a *AlipayPaymentAdapter) Supports(method biz.PaymentMethod) bool {
	method = method.Normalize()
	return method.Provider == a.Provider() && (method.Product == "wap" || method.Product == "app")
}
func (a *AlipayPaymentAdapter) Capabilities(biz.PaymentMethod) biz.PaymentCapabilities {
	return biz.PaymentCapabilities{SupportsNotify: true, RequiresPoll: false, SupportsClose: true, SupportsRefund: true}
}
func (a *AlipayPaymentAdapter) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("alipay")
	}
	if err := validateCNYAmount(req.Amount, req.Currency); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := make(gopay.BodyMap)
	body.Set("subject", req.Description).Set("out_trade_no", req.OutTradeNo).Set("total_amount", fenToYuan(req.Amount))
	if a.notifyURL != "" {
		body.Set("notify_url", a.notifyURL)
	}
	var payload string
	var actionType biz.PaymentActionType
	var err error
	switch req.Method.Product {
	case "wap":
		payload, err = a.client.TradeWapPay(ctx, body)
		actionType = biz.PaymentActionRedirect
	case "app":
		payload, err = a.client.TradeAppPay(ctx, body)
		actionType = biz.PaymentActionInvoke
	default:
		return nil, biz.ErrPaymentProviderUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &biz.PaymentPrepayResult{Action: paymentAction(actionType, map[string]string{"payload": payload})}, nil
}
func (a *AlipayPaymentAdapter) Query(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("alipay")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := make(gopay.BodyMap)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("trade_no", req.TransactionID)
	}
	response, err := a.client.TradeQuery(ctx, body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if response.ErrResponse.Code == alipayCodeTradeNotExist {
			return nil, biz.ErrProviderOrderNotExist
		}
		return nil, fmt.Errorf("alipay trade query failed: %s %s", response.ErrResponse.Code, response.ErrResponse.Message)
	}
	amount, err := yuanToFen(response.TotalAmount)
	if err != nil {
		return nil, err
	}
	state, description := mapAlipayTradeState(response.TradeStatus)
	return &biz.PaymentQueryResult{Method: req.Method, OutTradeNo: response.OutTradeNo, TransactionID: response.TradeNo,
		TradeState: state, TradeStateDesc: description, RawTradeState: response.TradeStatus, Amount: amount, Currency: biz.DefaultCurrency}, nil
}
func (a *AlipayPaymentAdapter) Close(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if a.tradeRequester == nil {
		return nil, paymentProviderNotConfigured("alipay")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := make(gopay.BodyMap)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("trade_no", req.TransactionID)
	}
	response, err := a.tradeRequester.TradeClose(ctx, body)
	var result *biz.PaymentCloseResult
	if response != nil {
		result = &biz.PaymentCloseResult{
			Method: req.Method, OutTradeNo: orEmpty(response.OutTradeNo, req.OutTradeNo),
			TransactionID: orEmpty(response.TradeNo, req.TransactionID), RawCode: response.ErrResponse.Code,
		}
	}
	if err != nil {
		return result, err
	}
	if response == nil {
		return nil, fmt.Errorf("alipay close returned empty response")
	}
	if response.StatusCode == http.StatusOK {
		result.Success = true
		return result, nil
	}
	if _, ok := alipayCodeAlreadyClosed[response.ErrResponse.Code]; ok {
		result.Success = true
		return result, nil
	}
	return result, fmt.Errorf("alipay close rejected: HTTP %d %s %s", response.StatusCode, response.ErrResponse.Code, response.ErrResponse.Message)
}

func (a *AlipayPaymentAdapter) Refund(ctx context.Context, req biz.PaymentRefundRequest) (*biz.PaymentRefundResult, error) {
	if a.tradeRequester == nil {
		return nil, paymentProviderNotConfigured("alipay")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := make(gopay.BodyMap)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("trade_no", req.TransactionID)
	}
	body.Set("refund_amount", fenToYuan(req.Amount)).Set("out_request_no", req.OutRefundNo)
	if req.Reason != "" {
		body.Set("refund_reason", req.Reason)
	}
	response, err := a.tradeRequester.TradeRefund(ctx, body)
	var result *biz.PaymentRefundResult
	if response != nil {
		result = &biz.PaymentRefundResult{
			Method: req.Method, OutTradeNo: orEmpty(response.OutTradeNo, req.OutTradeNo),
			TransactionID: orEmpty(response.TradeNo, req.TransactionID), OutRefundNo: req.OutRefundNo,
			Currency: req.Currency, FundChanged: strings.EqualFold(response.FundChange, "Y"),
			RawCode: response.ErrResponse.Code,
		}
	}
	if err != nil {
		return result, err
	}
	if response == nil {
		return nil, fmt.Errorf("alipay refund returned empty response")
	}
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("alipay refund rejected: HTTP %d %s %s", response.StatusCode, response.ErrResponse.Code, response.ErrResponse.Message)
	}
	if response.OutTradeNo != "" && req.OutTradeNo != "" && response.OutTradeNo != req.OutTradeNo {
		return result, fmt.Errorf("alipay refund out_trade_no mismatch")
	}
	if response.TradeNo != "" && req.TransactionID != "" && response.TradeNo != req.TransactionID {
		return result, fmt.Errorf("alipay refund trade_no mismatch")
	}
	amount, err := yuanToFen(response.RefundFee)
	if err != nil {
		return result, fmt.Errorf("parse alipay refund fee: %w", err)
	}
	result.Amount = amount
	if amount != req.Amount {
		return result, fmt.Errorf("alipay refund amount mismatch: got %d want %d", amount, req.Amount)
	}
	result.Success = true
	return result, nil
}
func (a *AlipayPaymentAdapter) ParseAndVerifyNotification(request *http.Request) (*biz.PaymentNotification, error) {
	if a.publicCertPath == "" || a.expectedAppID == "" {
		return nil, errors.ServiceUnavailable("PAYMENT_SIGNATURE_CONFIGURATION_MISSING", "alipay signature configuration is missing")
	}
	body, err := boundedRequestBody(request)
	if err != nil {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_BODY_INVALID", err.Error())
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	notify, err := alipay.ParseNotifyToBodyMap(request)
	if err != nil {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_PARSE_FAILED", err.Error())
	}
	valid, err := alipay.VerifySignWithCert(a.publicCertPath, notify)
	if err != nil || !valid {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_SIGNATURE_INVALID", "alipay notification signature is invalid")
	}
	if err := a.validateAlipayNotification(notify); err != nil {
		return nil, err
	}
	amount, err := yuanToFen(notify.GetString("total_amount"))
	if err != nil || amount <= 0 {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_AMOUNT_INVALID", "alipay total_amount is invalid")
	}
	return &biz.PaymentNotification{Provider: a.Provider(), ProviderEventID: notify.GetString("notify_id"), OutTradeNo: notify.GetString("out_trade_no"),
		TransactionID: notify.GetString("trade_no"), Amount: amount, Currency: biz.DefaultCurrency,
		PayloadHash: sha256Hex(body), VerifiedAt: time.Now().UTC()}, nil
}

func (a *AlipayPaymentAdapter) validateAlipayNotification(notify gopay.BodyMap) error {
	if notify.GetString("app_id") != a.expectedAppID {
		return errors.BadRequest("PAYMENT_NOTIFICATION_MERCHANT_MISMATCH", "alipay notification app_id does not match configuration")
	}
	status := notify.GetString("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" {
		return errors.BadRequest("PAYMENT_NOTIFICATION_NOT_SUCCESSFUL", "alipay notification is not a successful payment")
	}
	if notify.GetString("notify_id") == "" || notify.GetString("out_trade_no") == "" || notify.GetString("trade_no") == "" {
		return errors.BadRequest("PAYMENT_NOTIFICATION_IDENTITY_INVALID", "alipay notification trade identity is incomplete")
	}
	return nil
}

func validateCNYAmount(amount int64, currency string) error {
	if amount <= 0 {
		return errors.BadRequest("PAYMENT_AMOUNT_INVALID", "payment amount must be positive")
	}
	if strings.ToUpper(strings.TrimSpace(currency)) != biz.DefaultCurrency {
		return errors.BadRequest("PAYMENT_CURRENCY_UNSUPPORTED", "payment provider only supports CNY")
	}
	return nil
}

func paymentAction(actionType biz.PaymentActionType, payload any) biz.PaymentAction {
	encoded, _ := json.Marshal(payload)
	return biz.PaymentAction{Type: actionType, Payload: encoded}
}
func boundedRequestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, fmt.Errorf("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxNotificationBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxNotificationBody {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxNotificationBody)
	}
	return body, nil
}
func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type PaymentRepo struct {
	data *Data
	tx   biz.TxManager
	jobs biz.PaymentMQRepo
	log  *log.Helper
}

func NewPaymentRepo(data *Data, tx biz.TxManager, logger log.Logger) *PaymentRepo {
	return NewPaymentRepoWithJobs(data, tx, nil, logger)
}

func NewPaymentRepoWithJobs(data *Data, tx biz.TxManager, jobs biz.PaymentMQRepo, logger log.Logger) *PaymentRepo {
	return &PaymentRepo{data: data, tx: tx, jobs: jobs, log: log.NewHelper(logger)}
}

func (r *PaymentRepo) CreatePayment(ctx context.Context, args biz.CreatePaymentArgs) (*biz.PaymentDO, error) {
	var row db.Payment
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		order, err := q.GetOrderForUpdate(ctx, args.OrderID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrOrderNotFound
			}
			return err
		}
		if order.UserID != args.UserID || order.TotalAmountMinor != args.Amount || order.Currency != args.Currency {
			return biz.ErrPaymentConflict
		}
		if order.Status == biz.OrderStatusPaid || order.Status == biz.OrderStatusShipped || order.Status == biz.OrderStatusCompleted {
			return biz.ErrOrderAlreadyPaid
		}
		if order.Status != biz.OrderStatusPendingPayment {
			return biz.ErrPaymentStateConflict
		}
		if order.ExpiresAt.Valid && !time.Now().UTC().Before(order.ExpiresAt.Time) {
			return biz.ErrOrderExpired
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, order.ID)
		if err != nil {
			return err
		}
		for _, payment := range payments {
			if payment.ReconciliationStatus == biz.ReconciliationStatusRequired {
				return biz.ErrPaymentReconciliationRequired
			}
			switch payment.Status {
			case biz.PaymentStatusSuccess, biz.PaymentStatusRefunded:
				return biz.ErrOrderAlreadyPaid
			case biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending:
				if payment.PayChannel == args.Method {
					row = payment
					return nil
				}
				return biz.ErrOrderHasActivePayment
			}
		}
		row, err = q.CreatePaymentWithOutTradeNo(ctx, db.CreatePaymentWithOutTradeNoParams{
			OrderID: args.OrderID, UserID: args.UserID, MerchantID: args.MerchantID, AmountMinor: args.Amount,
			Currency: args.Currency, Status: biz.PaymentStatusCreating, PayChannel: args.Method,
			OutTradeNo: args.OutTradeNo,
		})
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if stderrors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_payments_one_active_per_order" {
			existing, getErr := r.getActivePaymentByOrder(ctx, args.OrderID)
			if getErr == nil {
				if existing.Method == args.Method {
					return existing, nil
				}
				return nil, biz.ErrOrderHasActivePayment
			}
			return nil, biz.ErrPaymentConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.cachePayment(ctx, payment)
	return payment, nil
}

func (r *PaymentRepo) ClaimPaymentPrepay(ctx context.Context, id int64, token string, leaseDuration time.Duration) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).ClaimPaymentPrepay(ctx, db.ClaimPaymentPrepayParams{
		ID: id, PrepayLeaseToken: pgtype.Text{String: token, Valid: token != ""},
		LeaseSeconds: leaseDuration.Seconds(),
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			currentRow, loadErr := querierFromContext(ctx, r.data.q).GetPayment(ctx, id)
			if loadErr != nil {
				return nil, loadErr
			}
			current := toBizPayment(currentRow)
			if current.Status == biz.PaymentStatusPending && current.Action.Type != "" {
				return current, nil
			}
			if current.Status == biz.PaymentStatusCreating && current.PrepayLeaseUntil != nil && current.PrepayLeaseUntil.After(time.Now()) {
				return nil, biz.ErrPaymentPrepayInProgress
			}
			return nil, biz.ErrPaymentStateConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.invalidatePayment(ctx, row)
	r.cachePayment(ctx, payment)
	return payment, nil
}

func (r *PaymentRepo) FinalizePaymentPrepay(ctx context.Context, id int64, token string, action biz.PaymentAction) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).FinalizePaymentPrepay(ctx, db.FinalizePaymentPrepayParams{
		ID: id, PrepayLeaseToken: pgtype.Text{String: token, Valid: token != ""},
		ActionType:    pgtype.Text{String: string(action.Type), Valid: action.Type != ""},
		ActionPayload: action.Payload,
	})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			currentRow, loadErr := querierFromContext(ctx, r.data.q).GetPayment(ctx, id)
			if loadErr != nil {
				return nil, loadErr
			}
			current := toBizPayment(currentRow)
			if current.Status == biz.PaymentStatusPending && current.Action.Type != "" {
				return current, nil
			}
			return nil, biz.ErrPaymentStateConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.invalidatePayment(ctx, row)
	r.cachePayment(ctx, payment)
	return payment, nil
}

func (r *PaymentRepo) RecordPaymentPrepayError(ctx context.Context, id int64, token, lastError string) error {
	_, err := querierFromContext(ctx, r.data.q).RecordPaymentPrepayError(ctx, db.RecordPaymentPrepayErrorParams{
		ID: id, PrepayLeaseToken: pgtype.Text{String: token, Valid: token != ""},
		LastError: pgtype.Text{String: lastError, Valid: lastError != ""},
	})
	return err
}

func (r *PaymentRepo) GetPayment(ctx context.Context, id int64) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", id), func() (db.Payment, error) { return querierFromContext(ctx, r.data.q).GetPayment(ctx, id) })
}
func (r *PaymentRepo) GetPaymentByUser(ctx context.Context, id, userID int64) (*biz.PaymentDO, error) {
	payment, err := r.GetPayment(ctx, id)
	if err != nil {
		return nil, err
	}
	if payment.UserID != userID {
		return nil, biz.ErrPaymentNotFound
	}
	return payment, nil
}
func (r *PaymentRepo) GetLatestPaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "order", orderID), func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetLatestPaymentByOrder(ctx, orderID)
	})
}
func (r *PaymentRepo) GetActivePaymentByOrderMethod(ctx context.Context, orderID int64, method string) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "order", orderID, "active", method), func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetActivePaymentByOrderChannel(ctx, db.GetActivePaymentByOrderChannelParams{OrderID: orderID, PayChannel: method})
	})
}
func (r *PaymentRepo) getActivePaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).GetActivePaymentByOrder(ctx, orderID)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPaymentNotFound
	}
	if err != nil {
		return nil, err
	}
	return toBizPayment(row), nil
}
func (r *PaymentRepo) GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "out_trade_no", outTradeNo), func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetPaymentByOutTradeNo(ctx, outTradeNo)
	})
}

func (r *PaymentRepo) PreparePaymentRefund(ctx context.Context, paymentID int64, outRefundNo string) (*biz.PaymentDO, *biz.PaymentRefund, error) {
	var payment db.Payment
	var refund db.OrderRefund
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		var err error
		payment, err = q.GetPaymentForUpdate(ctx, paymentID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrPaymentNotFound
			}
			return err
		}
		refund, err = q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: paymentID, Valid: true})
		if err == nil {
			if refund.OrderID != payment.OrderID || refund.UserID != payment.UserID ||
				refund.TotalAmountMinor != payment.AmountMinor || refund.RefundAmountMinor != payment.AmountMinor ||
				refund.Currency != payment.Currency {
				return biz.ErrPaymentStateConflict
			}
			if payment.Status == biz.PaymentStatusRefunded && refund.Status != biz.PaymentRefundStatusSuccess {
				return biz.ErrPaymentStateConflict
			}
			if payment.Status != biz.PaymentStatusSuccess && payment.Status != biz.PaymentStatusRefunded {
				return biz.ErrPaymentStateConflict
			}
			return nil
		}
		if !stderrors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if payment.Status != biz.PaymentStatusSuccess {
			return biz.ErrPaymentStateConflict
		}
		if strings.TrimSpace(outRefundNo) == "" {
			return errors.BadRequest("OUT_REFUND_NO_REQUIRED", "out_refund_no is required")
		}
		refund, err = q.CreateOrderRefund(ctx, db.CreateOrderRefundParams{
			PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true},
			OrderID:   payment.OrderID, UserID: payment.UserID, OutRefundNo: outRefundNo,
			TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor,
			Currency: payment.Currency,
		})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return toBizPayment(payment), toBizPaymentRefund(refund), nil
}

func (r *PaymentRepo) RecordPaymentRefundError(ctx context.Context, refundID int64, lastError string, definitive bool) error {
	_, err := querierFromContext(ctx, r.data.q).RecordOrderRefundError(ctx, db.RecordOrderRefundErrorParams{
		Definitive: definitive, LastError: lastError, ID: refundID,
	})
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (r *PaymentRepo) ApplyPaymentRefund(ctx context.Context, paymentID, refundID int64) error {
	var changed db.Payment
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		current, err := q.GetPaymentForUpdate(ctx, paymentID)
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrPaymentNotFound
			}
			return err
		}
		refund, err := q.GetOrderRefundByPaymentID(ctx, pgtype.Int8{Int64: paymentID, Valid: true})
		if err != nil {
			return err
		}
		if refund.ID != refundID {
			return biz.ErrPaymentStateConflict
		}
		if current.Status == biz.PaymentStatusRefunded && refund.Status == biz.PaymentRefundStatusSuccess {
			changed = current
			return nil
		}
		if current.Status != biz.PaymentStatusSuccess {
			return biz.ErrPaymentStateConflict
		}
		if _, err := q.MarkOrderRefundSuccess(ctx, db.MarkOrderRefundSuccessParams{
			ID: refundID, PaymentID: pgtype.Int8{Int64: paymentID, Valid: true},
		}); err != nil {
			return err
		}
		changed, err = q.ConfirmPaymentRefunded(ctx, paymentID)
		return err
	})
	if err != nil {
		return err
	}
	r.invalidatePayment(ctx, changed)
	observability.PaymentTransition(ctx, biz.PaymentStatusSuccess, biz.PaymentStatusRefunded, "provider_refund", strings.SplitN(changed.PayChannel, ":", 2)[0])
	return nil
}

func (r *PaymentRepo) BeginPaymentNotificationProcessing(ctx context.Context, id int64, provider, outTradeNo string) (bool, error) {
	if id <= 0 {
		return true, nil
	}
	q := querierFromContext(ctx, r.data.q)
	notification, err := q.GetPaymentNotification(ctx, id)
	if err != nil {
		return false, err
	}
	if notification.Provider != provider || notification.OutTradeNo != outTradeNo {
		return false, biz.ErrPaymentNotificationBinding
	}
	if notification.Status == biz.PaymentNotificationStatusProcessed {
		return false, nil
	}
	if _, err := q.BeginPaymentNotificationProcessing(ctx, id); err != nil {
		return false, err
	}
	return true, nil
}

func (r *PaymentRepo) RecordPaymentNotificationError(ctx context.Context, id int64, lastError string) error {
	if id <= 0 {
		return nil
	}
	_, err := querierFromContext(ctx, r.data.q).RecordPaymentNotificationError(ctx, db.RecordPaymentNotificationErrorParams{
		ID: id, LastError: notificationErrorText(lastError),
	})
	return err
}

func (r *PaymentRepo) MarkPaymentNotificationFailed(ctx context.Context, id int64, lastError string) error {
	if id <= 0 {
		return nil
	}
	q := querierFromContext(ctx, r.data.q)
	rows, err := q.MarkPaymentNotificationFailed(ctx, db.MarkPaymentNotificationFailedParams{
		ID: id, LastError: notificationErrorText(lastError),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return notificationStateAfterCAS(ctx, q, id, biz.PaymentNotificationStatusProcessed)
	}
	return nil
}

func notificationErrorText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
func (r *PaymentRepo) getPayment(ctx context.Context, key string, load func() (db.Payment, error)) (*biz.PaymentDO, error) {
	if cached, err := r.getCache(ctx, key); err == nil {
		return cached, nil
	} else if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorw("msg", "read payment cache failed", "key", key, "error", err)
	}
	value, err, _ := r.data.sg.Do("sf:"+key, func() (any, error) {
		if cached, err := r.getCache(ctx, key); err == nil {
			return cached, nil
		}
		row, err := load()
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return nil, biz.ErrPaymentNotFound
			}
			return nil, err
		}
		payment := toBizPayment(row)
		r.setCache(ctx, key, payment)
		return payment, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*biz.PaymentDO), nil
}

func (r *PaymentRepo) ApplyPayQuery(ctx context.Context, args biz.CheckPayArgs, result *biz.PaymentQueryResult) error {
	var changed db.Payment
	var fromStatus, provider, event string
	var orderID int64
	orderCancelled := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		snapshot, err := q.GetPayment(ctx, args.PaymentID)
		if err != nil {
			return err
		}
		orderID = snapshot.OrderID
		order, err := q.GetOrderForUpdate(ctx, snapshot.OrderID)
		if err != nil {
			return err
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, snapshot.OrderID)
		if err != nil {
			return err
		}
		var payment db.Payment
		found := false
		for _, candidate := range payments {
			if candidate.ID == args.PaymentID {
				payment = candidate
				found = true
				break
			}
		}
		if !found {
			return biz.ErrPaymentNotFound
		}
		method, parseErr := biz.ParsePaymentMethod(payment.PayChannel)
		if parseErr != nil {
			return parseErr
		}
		if mismatch := validateProviderResult(payment, method, result); mismatch != "" {
			reason := reconciliationReasonForMismatch(mismatch)
			provider, fromStatus, event = method.Provider, payment.Status, reason
			if mismatch == "amount mismatch" {
				observability.PaymentAmountMismatch(ctx, method.Provider)
			}
			observability.PaymentReconcileRequired(ctx, method.Provider)
			changed, err = requirePaymentReconciliation(ctx, q, payment.ID, reason, mismatch)
			if err != nil {
				return err
			}
			if recordErr := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
				PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
				Attempt: max(1, args.PollCount), Reason: reason, LastError: mismatch,
			}); recordErr != nil {
				return recordErr
			}
			r.log.WithContext(ctx).Errorw("msg", "payment provider result mismatch", "event", "payment_reconcile_required", "payment_id", payment.ID, "provider", method.Provider, "reason", mismatch)
			return markNotificationProcessed(ctx, q, args.NotificationID)
		}
		switch result.TradeState {
		case biz.TradeStateSuccess:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_success"
			if result.TransactionID == "" {
				return errors.BadRequest("PAYMENT_TRANSACTION_ID_REQUIRED", "successful payment requires transaction id")
			}
			if payment.Status == biz.PaymentStatusSuccess && payment.ThirdPartyTxID.String == result.TransactionID {
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			if payment.Status == biz.PaymentStatusSuccess {
				changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "provider_mismatch", "successful payment transaction id changed")
				if err != nil {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
					Attempt: max(1, args.PollCount), Reason: "provider_mismatch",
					LastError: "successful payment transaction id changed",
				}); err != nil {
					return err
				}
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			if payment.Status == biz.PaymentStatusRefunded {
				return biz.ErrPaymentStateConflict
			}
			otherSuccess := false
			for _, candidate := range payments {
				if candidate.ID != payment.ID && (candidate.Status == biz.PaymentStatusSuccess || candidate.Status == biz.PaymentStatusRefunded) {
					otherSuccess = true
					break
				}
			}
			changed, err = q.RecordPaymentSuccess(ctx, db.RecordPaymentSuccessParams{
				ID: payment.ID, ThirdPartyTxID: pgtype.Text{String: result.TransactionID, Valid: true},
			})
			if err != nil {
				return err
			}
			if otherSuccess {
				event = "duplicate_success"
				changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "duplicate_success", "another payment already completed this order")
				if err != nil {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
					Attempt: max(1, args.PollCount), Reason: "duplicate_success", LastError: "another payment already completed this order",
				}); err != nil {
					return err
				}
				observability.PaymentConflict(ctx, method.Provider)
				observability.PaymentReconcileRequired(ctx, method.Provider)
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			switch order.Status {
			case biz.OrderStatusPendingPayment:
				if _, err = q.MarkOrderPaid(ctx, payment.OrderID); err != nil {
					return err
				}
			case biz.OrderStatusCancelling, biz.OrderStatusCancelled:
				event = "late_success_after_cancel"
				changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "late_success_after_cancel", "payment succeeded after order cancellation started")
				if err != nil {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
					PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
					Attempt: max(1, args.PollCount), Reason: "late_success_after_cancel",
					LastError: "payment succeeded after order cancellation started",
				}); err != nil {
					return err
				}
				observability.PaymentReconcileRequired(ctx, method.Provider)
			case biz.OrderStatusPaid, biz.OrderStatusShipped, biz.OrderStatusCompleted:
				// The target payment is the already-recorded successful payment.
			default:
				return biz.ErrPaymentStateConflict
			}
		case biz.TradeStateClosed, biz.TradeStateRevoked:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_closed"
			if payment.Status == biz.PaymentStatusClosed {
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			changed, err = q.MarkPaymentClosed(ctx, payment.ID)
			if err != nil {
				return paymentStateAfterCAS(ctx, q, payment.ID, biz.PaymentStatusClosed)
			}
			orderCancelled, err = finalizeOrderAfterPaymentInactive(ctx, q, order, payments, payment)
			if err != nil {
				return err
			}
		case biz.TradeStatePayError:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_failed"
			changed, err = q.MarkPaymentFailed(ctx, db.MarkPaymentFailedParams{
				ID: payment.ID, LastError: pgtype.Text{String: result.TradeStateDesc, Valid: result.TradeStateDesc != ""},
			})
			if stderrors.Is(err, pgx.ErrNoRows) {
				// Idempotent retry: the payment already left the active set.
				if payment.Status != biz.PaymentStatusFailed && payment.Status != biz.PaymentStatusClosed {
					return biz.ErrPaymentStateConflict
				}
				err = nil
			}
			if err != nil {
				return err
			}
			// A failed close_pending payment comes from the close flow (order
			// expiry, api close, or poll exhaustion); nobody revisits the order
			// afterwards, so settle it here exactly like a provider close.
			if payment.Status == biz.PaymentStatusClosePending || args.Trigger == "close_pay" {
				orderCancelled, err = finalizeOrderAfterPaymentInactive(ctx, q, order, payments, payment)
				if err != nil {
					return err
				}
			}
		case biz.TradeStateRefund:
			provider, fromStatus, event = method.Provider, payment.Status, "provider_refund"
			if payment.Status == biz.PaymentStatusRefunded {
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			if payment.Status != biz.PaymentStatusSuccess {
				return biz.ErrPaymentStateConflict
			}
			if err := q.UpdatePaymentRefunded(ctx, payment.ID); err != nil {
				return err
			}
			changed = payment
			changed.Status = biz.PaymentStatusRefunded
		default:
			provider, fromStatus, event = method.Provider, payment.Status, "unknown_provider_state"
			changed, err = requirePaymentReconciliation(ctx, q, payment.ID, "unknown_provider_state", result.RawTradeState)
			if err != nil {
				return err
			}
			if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{
				PaymentID: payment.ID, NotificationID: args.NotificationID, Provider: method.Provider,
				Attempt: max(1, args.PollCount), Reason: "unknown_provider_state",
				LastError: "unsupported provider state " + result.RawTradeState,
			}); err != nil {
				return err
			}
		}
		return markNotificationProcessed(ctx, q, args.NotificationID)
	})
	if err == nil && changed.ID > 0 {
		observability.PaymentTransition(ctx, fromStatus, changed.Status, event, provider)
		r.invalidatePayment(ctx, changed)
		r.invalidateOrder(ctx, changed.OrderID)
	}
	if err == nil && orderCancelled {
		if changed.ID == 0 {
			r.invalidateOrder(ctx, orderID)
		}
		invalidateProductCachesForOrder(ctx, r.data, r.log, orderID)
	}
	return err
}

// finalizeOrderAfterPaymentInactive settles the order after `current` left the
// active payment set (closed or failed): heal the order to paid when a sibling
// payment already succeeded, or cancel it and restore stock when nothing else
// can still pay for it. Returns true when the order was cancelled so callers
// can invalidate product caches after the transaction commits.
func finalizeOrderAfterPaymentInactive(ctx context.Context, q db.Querier, order db.Order, payments []db.Payment, current db.Payment) (bool, error) {
	hasSuccess, hasActive := false, false
	hasReconciliation := current.ReconciliationStatus == biz.ReconciliationStatusRequired
	for _, candidate := range payments {
		if candidate.ID == current.ID {
			continue
		}
		if candidate.ReconciliationStatus == biz.ReconciliationStatusRequired {
			hasReconciliation = true
		}
		switch candidate.Status {
		case biz.PaymentStatusSuccess, biz.PaymentStatusRefunded:
			hasSuccess = true
		case biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending:
			hasActive = true
		}
	}
	if hasSuccess {
		if order.Status == biz.OrderStatusPendingPayment {
			if _, err := q.MarkOrderPaid(ctx, order.ID); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if !hasActive && !hasReconciliation && order.Status == biz.OrderStatusPendingPayment {
		if _, err := q.MarkOrderCancelling(ctx, order.ID); err != nil {
			return false, err
		}
		if err := q.RestoreOrderItemStock(ctx, order.ID); err != nil {
			return false, err
		}
		if _, err := q.MarkOrderCancelled(ctx, order.ID); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func reconciliationReasonForMismatch(mismatch string) string {
	switch mismatch {
	case "amount mismatch":
		return "amount_mismatch"
	case "currency mismatch":
		return "currency_mismatch"
	default:
		return "provider_mismatch"
	}
}

func requirePaymentReconciliation(ctx context.Context, q db.Querier, paymentID int64, reason, detail string) (db.Payment, error) {
	return q.RequirePaymentReconciliation(ctx, db.RequirePaymentReconciliationParams{
		ID:                   paymentID,
		ReconciliationReason: pgtype.Text{String: reason, Valid: reason != ""},
		ReconciliationDetail: pgtype.Text{String: detail, Valid: detail != ""},
	})
}

func validateProviderResult(payment db.Payment, method biz.PaymentMethod, result *biz.PaymentQueryResult) string {
	if result == nil {
		return "empty provider result"
	}
	if result.OutTradeNo != payment.OutTradeNo {
		return "out_trade_no mismatch"
	}
	if result.Method.Normalize().String() != method.String() {
		return "payment method mismatch"
	}
	// Amount and currency only matter when money moved; closed or failed
	// trades may omit them and must not be blocked on reconciliation.
	if result.TradeState == biz.TradeStateSuccess || result.TradeState == biz.TradeStateRefund {
		if result.Amount != payment.AmountMinor {
			return "amount mismatch"
		}
		if strings.ToUpper(result.Currency) != payment.Currency {
			return "currency mismatch"
		}
	}
	return ""
}
func paymentStateAfterCAS(ctx context.Context, q db.Querier, id int64, desired string) error {
	current, err := q.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == desired {
		return nil
	}
	return biz.ErrPaymentStateConflict
}
func notificationStateAfterCAS(ctx context.Context, q db.Querier, id int64, desired string) error {
	current, err := q.GetPaymentNotification(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == desired {
		return nil
	}
	return errors.Conflict("PAYMENT_NOTIFICATION_STATE_CONFLICT", "payment notification state transition conflicted")
}
func markNotificationProcessed(ctx context.Context, q db.Querier, id int64) error {
	if id <= 0 {
		return nil
	}
	rows, err := q.MarkPaymentNotificationProcessed(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return notificationStateAfterCAS(ctx, q, id, biz.PaymentNotificationStatusProcessed)
	}
	return nil
}

func (r *PaymentRepo) MarkPayClosePending(ctx context.Context, args biz.CheckPayArgs) error {
	var changed db.Payment
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		row, err := q.MarkPaymentClosePending(ctx, args.PaymentID)
		if err != nil {
			if !stderrors.Is(err, pgx.ErrNoRows) {
				return err
			}
			current, loadErr := q.GetPayment(ctx, args.PaymentID)
			if loadErr != nil {
				return loadErr
			}
			if current.Status != biz.PaymentStatusClosePending {
				return biz.ErrPaymentStateConflict
			}
			row = current
		}
		method, err := biz.ParsePaymentMethod(row.PayChannel)
		if err != nil {
			return err
		}
		if r.jobs == nil {
			return fmt.Errorf("payment mq is not configured")
		}
		if _, err := r.jobs.EnqueueClosePayTx(ctx, biz.ClosePayArgs{
			PaymentID: row.ID, Provider: method.Provider, Reason: args.Trigger,
		}, time.Time{}); err != nil {
			return err
		}
		if err := markNotificationProcessed(ctx, q, args.NotificationID); err != nil {
			return err
		}
		changed = row
		return nil
	})
	if err == nil && changed.ID > 0 {
		r.invalidatePayment(ctx, changed)
	}
	return err
}
func (r *PaymentRepo) MarkReconciliationRequired(ctx context.Context, failure biz.ReconciliationFailure) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		reason := failure.Reason
		if reason == "" {
			reason = "job_exhausted"
		}
		row, err := requirePaymentReconciliation(ctx, q, failure.PaymentID, reason, failure.LastError)
		if err != nil {
			return err
		}
		if row.ID > 0 {
			r.invalidatePayment(ctx, row)
		}
		if err := createReconciliationFailure(ctx, q, failure); err != nil {
			return err
		}
		return markNotificationProcessed(ctx, q, failure.NotificationID)
	})
}
func (r *PaymentRepo) RecordReconciliationFailure(ctx context.Context, failure biz.ReconciliationFailure) error {
	return createReconciliationFailure(ctx, querierFromContext(ctx, r.data.q), failure)
}
func createReconciliationFailure(ctx context.Context, q db.Querier, failure biz.ReconciliationFailure) error {
	jobID := pgtype.Int8{}
	if failure.RiverJobID != nil {
		jobID = pgtype.Int8{Int64: *failure.RiverJobID, Valid: true}
	}
	reason := failure.Reason
	if reason == "" {
		reason = "job_exhausted"
	}
	_, err := q.CreatePaymentReconciliationFailure(ctx, db.CreatePaymentReconciliationFailureParams{
		PaymentID: failure.PaymentID, Provider: failure.Provider, Reason: reason,
		RiverJobID: jobID, Attempt: int32(max(1, failure.Attempt)), LastError: failure.LastError,
	})
	return err
}

func (r *PaymentRepo) getCache(ctx context.Context, key string) (*biz.PaymentDO, error) {
	value, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var payment biz.PaymentDO
	if err := json.Unmarshal(value, &payment); err != nil {
		return nil, err
	}
	return &payment, nil
}
func (r *PaymentRepo) setCache(ctx context.Context, key string, payment *biz.PaymentDO) {
	afterCommit(ctx, func() {
		value, err := json.Marshal(payment)
		if err == nil {
			err = r.data.rdb.Set(ctx, key, value, 15*time.Minute).Err()
		}
		if err != nil {
			r.log.WithContext(ctx).Errorw("msg", "write payment cache failed", "key", key, "error", err)
		}
	})
}
func (r *PaymentRepo) cachePayment(ctx context.Context, payment *biz.PaymentDO) {
	r.setCache(ctx, redisKey("payment", payment.ID), payment)
	r.setCache(ctx, redisKey("payment", "out_trade_no", payment.OutTradeNo), payment)
	r.setCache(ctx, redisKey("payment", "order", payment.OrderID), payment)
	r.setCache(ctx, redisKey("payment", "order", payment.OrderID, "active", payment.Method), payment)
}
func (r *PaymentRepo) invalidatePayment(ctx context.Context, payment db.Payment) {
	r.deleteCache(ctx, redisKey("payment", payment.ID))
	r.deleteCache(ctx, redisKey("payment", "order", payment.OrderID))
	r.deleteCache(ctx, redisKey("payment", "order", payment.OrderID, "active", payment.PayChannel))
	if payment.OutTradeNo != "" {
		r.deleteCache(ctx, redisKey("payment", "out_trade_no", payment.OutTradeNo))
	}
}
func (r *PaymentRepo) invalidateOrder(ctx context.Context, orderID int64) {
	order, err := querierFromContext(ctx, r.data.q).GetOrder(ctx, orderID)
	if err != nil {
		return
	}
	r.deleteCache(ctx, redisKey("order", orderID))
	r.deleteCache(ctx, redisKey("order", "user", orderID, order.UserID))
	if order.OutTradeNo != "" {
		r.deleteCache(ctx, redisKey("order", "no", order.OutTradeNo))
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("order", "user", order.UserID, "gen"))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("order", "user", "ongoing", order.UserID, "gen"))
}
func (r *PaymentRepo) deleteCache(ctx context.Context, key string) {
	afterCommit(ctx, func() {
		if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
			r.log.WithContext(ctx).Errorw("msg", "delete cache failed", "key", key, "error", err)
		}
	})
}

func toBizPayment(row db.Payment) *biz.PaymentDO {
	payment := &biz.PaymentDO{
		ID: row.ID, OrderID: row.OrderID, UserID: row.UserID, MerchantID: row.MerchantID,
		Amount: row.AmountMinor, Currency: row.Currency, Status: row.Status, Method: row.PayChannel,
		OutTradeNo: row.OutTradeNo, ThirdPartyTxID: row.ThirdPartyTxID.String,
		ReconciliationStatus: row.ReconciliationStatus, ReconciliationReason: row.ReconciliationReason.String,
		ReconciliationDetail: row.ReconciliationDetail.String, PrepayLeaseToken: row.PrepayLeaseToken.String,
		PrepayAttempts: row.PrepayAttempts, LastError: row.LastError.String,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ActionType.Valid {
		payment.Action.Type = biz.PaymentActionType(row.ActionType.String)
		payment.Action.Payload = append(json.RawMessage(nil), row.ActionPayload...)
	}
	if row.PaidAt.Valid {
		paidAt := row.PaidAt.Time
		payment.PaidAt = &paidAt
	}
	if row.PrepayLeaseUntil.Valid {
		leaseUntil := row.PrepayLeaseUntil.Time
		payment.PrepayLeaseUntil = &leaseUntil
	}
	return payment
}

func toBizPaymentRefund(row db.OrderRefund) *biz.PaymentRefund {
	return &biz.PaymentRefund{
		ID: row.ID, PaymentID: row.PaymentID.Int64, OrderID: row.OrderID, UserID: row.UserID,
		OutRefundNo: row.OutRefundNo, TotalAmount: row.TotalAmountMinor, RefundAmount: row.RefundAmountMinor,
		Currency: row.Currency, Reason: row.Reason, Status: row.Status, LastError: row.LastError,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

type PaymentMQRepo struct {
	client *river.Client[pgx.Tx]
	log    *log.Helper
}

func NewPaymentMQRepo(client *river.Client[pgx.Tx], logger log.Logger) *PaymentMQRepo {
	return &PaymentMQRepo{client: client, log: log.NewHelper(logger)}
}
func NewPaymentMQRepoForWire(insertClient *RiverInsertClient, logger log.Logger) *PaymentMQRepo {
	return NewPaymentMQRepo(insertClient.client, logger)
}
func (r *PaymentMQRepo) insert(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time, tx pgx.Tx) (*biz.MQJob, error) {
	opts := r.checkPayInsertOpts(args, scheduledAt)
	var result *rivertype.JobInsertResult
	var err error
	if tx == nil {
		result, err = r.client.Insert(ctx, args, opts)
	} else {
		result, err = r.client.InsertTx(ctx, tx, args, opts)
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river insert returned no job")
	}
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infow("msg", "deduplicated active reconciliation job", "job_id", result.Job.ID, "payment_id", args.PaymentID)
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}
func (r *PaymentMQRepo) EnqueueCheckPay(ctx context.Context, args biz.CheckPayArgs, at time.Time) (*biz.MQJob, error) {
	return r.insert(ctx, args, at, nil)
}
func (r *PaymentMQRepo) EnqueueCheckPayTx(ctx context.Context, args biz.CheckPayArgs, at time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("missing transaction")
	}
	return r.insert(ctx, args, at, tx)
}
func (r *PaymentMQRepo) EnqueueExpireOrderTx(ctx context.Context, args biz.ExpireOrderArgs, at time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("missing transaction")
	}
	opts := &river.InsertOpts{
		MaxAttempts: 8,
		Queue:       "orders",
		Tags:        []string{fmt.Sprintf("order-%d", args.OrderID)},
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
	if !at.IsZero() {
		opts.ScheduledAt = at
	}
	result, err := r.client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river expire order insert returned no job")
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}
func (r *PaymentMQRepo) EnqueueClosePay(ctx context.Context, args biz.ClosePayArgs, at time.Time) (*biz.MQJob, error) {
	return r.insertClosePay(ctx, args, at, nil)
}
func (r *PaymentMQRepo) EnqueueClosePayTx(ctx context.Context, args biz.ClosePayArgs, at time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("missing transaction")
	}
	return r.insertClosePay(ctx, args, at, tx)
}
func (r *PaymentMQRepo) insertClosePay(ctx context.Context, args biz.ClosePayArgs, at time.Time, tx pgx.Tx) (*biz.MQJob, error) {
	opts := &river.InsertOpts{
		MaxAttempts: 8,
		Queue:       "payments",
		Tags:        []string{fmt.Sprintf("provider-%s", args.Provider), fmt.Sprintf("payment-%d", args.PaymentID)},
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
	if !at.IsZero() {
		opts.ScheduledAt = at
	}
	var result *rivertype.JobInsertResult
	var err error
	if tx == nil {
		result, err = r.client.Insert(ctx, args, opts)
	} else {
		result, err = r.client.InsertTx(ctx, tx, args, opts)
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river close payment insert returned no job")
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}
func (r *PaymentMQRepo) checkPayInsertOpts(args biz.CheckPayArgs, at time.Time) *river.InsertOpts {
	opts := &river.InsertOpts{MaxAttempts: 8, Queue: "payments", Tags: []string{fmt.Sprintf("provider-%s", args.Provider), fmt.Sprintf("payment-%d", args.PaymentID)}, UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}
	if !at.IsZero() {
		opts.ScheduledAt = at
	}
	return opts
}
func (r *PaymentMQRepo) GetMQJob(ctx context.Context, id int64) (*biz.MQJob, error) {
	row, err := r.client.JobGet(ctx, id)
	if stderrors.Is(err, rivertype.ErrNotFound) {
		return nil, errors.NotFound("MQ_JOB_NOT_FOUND", "mq job not found")
	}
	if err != nil {
		return nil, err
	}
	return toBizMQJob(row), nil
}
func toBizMQJob(row *rivertype.JobRow) *biz.MQJob {
	if row == nil {
		return nil
	}
	result := &biz.MQJob{ID: row.ID, Kind: row.Kind, Queue: row.Queue, State: string(row.State), Attempt: row.Attempt, MaxAttempts: row.MaxAttempts, ArgsJSON: string(row.EncodedArgs), Tags: row.Tags, CreatedAt: row.CreatedAt, ScheduledAt: row.ScheduledAt, AttemptedAt: row.AttemptedAt, FinalizedAt: row.FinalizedAt, Errors: make([]biz.MQJobError, len(row.Errors))}
	for i, item := range row.Errors {
		result.Errors[i] = biz.MQJobError{Attempt: item.Attempt, Error: item.Error, At: item.At}
	}
	return result
}

type PaymentNotificationRepo struct {
	tx   biz.TxManager
	jobs biz.PaymentMQRepo
}

func NewPaymentNotificationRepo(tx biz.TxManager, jobs biz.PaymentMQRepo) *PaymentNotificationRepo {
	return &PaymentNotificationRepo{tx: tx, jobs: jobs}
}
func (r *PaymentNotificationRepo) PersistAndEnqueueNotification(ctx context.Context, notification *biz.PaymentNotification, args biz.CheckPayArgs) (bool, error) {
	duplicate := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		row, err := q.CreatePaymentNotification(ctx, db.CreatePaymentNotificationParams{Provider: notification.Provider, ProviderEventID: pgtype.Text{String: notification.ProviderEventID, Valid: notification.ProviderEventID != ""}, OutTradeNo: notification.OutTradeNo, PayloadHash: notification.PayloadHash, VerifiedAt: pgtype.Timestamptz{Time: notification.VerifiedAt, Valid: true}})
		if stderrors.Is(err, pgx.ErrNoRows) {
			duplicate = true
			row, err = getExistingPaymentNotification(ctx, q, notification)
		}
		if err != nil {
			return err
		}
		if row.Provider != notification.Provider || row.OutTradeNo != notification.OutTradeNo || row.PayloadHash != notification.PayloadHash {
			return errors.Conflict("PAYMENT_NOTIFICATION_IDENTITY_CONFLICT", "payment notification identity conflicts with an existing notification")
		}
		if row.Status == biz.PaymentNotificationStatusProcessed {
			return nil
		}
		args = biz.NormalizeCheckPayArgs(args)
		args.NotificationID = row.ID
		job, err := r.jobs.EnqueueCheckPayTx(ctx, args, time.Time{})
		if err != nil {
			return err
		}
		if job == nil || job.ID <= 0 {
			return fmt.Errorf("payment notification enqueue returned an empty job")
		}
		return q.SetPaymentNotificationRiverJob(ctx, db.SetPaymentNotificationRiverJobParams{
			ID: row.ID, RiverJobID: pgtype.Int8{Int64: job.ID, Valid: true},
		})
	})
	return duplicate, err
}

func getExistingPaymentNotification(ctx context.Context, q db.Querier, notification *biz.PaymentNotification) (db.PaymentNotification, error) {
	if notification.ProviderEventID != "" {
		row, err := q.GetPaymentNotificationByEvent(ctx, db.GetPaymentNotificationByEventParams{
			Provider:        notification.Provider,
			ProviderEventID: pgtype.Text{String: notification.ProviderEventID, Valid: true},
		})
		if err == nil || !stderrors.Is(err, pgx.ErrNoRows) {
			return row, err
		}
	}
	return q.GetPaymentNotificationByPayload(ctx, db.GetPaymentNotificationByPayloadParams{
		Provider: notification.Provider, OutTradeNo: notification.OutTradeNo, PayloadHash: notification.PayloadHash,
	})
}

func paymentProviderNotConfigured(provider string) error {
	return errors.ServiceUnavailable("PAYMENT_PROVIDER_NOT_AVAILABLE", provider+" payment provider is not configured")
}
func fenToYuan(amount int64) string { return decimal.NewFromInt(amount).Shift(-2).StringFixed(2) }
func yuanToFen(value string) (int64, error) {
	amount, err := decimal.NewFromString(value)
	if err != nil {
		return 0, err
	}
	minor := amount.Shift(2)
	if !minor.Equal(minor.Truncate(0)) {
		return 0, fmt.Errorf("amount has sub-minor precision")
	}
	return minor.IntPart(), nil
}
func mapAlipayTradeState(state string) (biz.TradeState, string) {
	switch strings.ToUpper(state) {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		return biz.TradeStateSuccess, state
	case "WAIT_BUYER_PAY":
		return biz.TradeStateNotPay, state
	case "TRADE_CLOSED":
		return biz.TradeStateClosed, state
	default:
		return biz.TradeStateUnspecified, state
	}
}
func orEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}
