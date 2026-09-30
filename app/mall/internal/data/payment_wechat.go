package data

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/wechat"
	"github.com/go-pay/util"
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
		if errCode == "ORDERNOTEXIST" {
			return biz.ErrProviderOrderNotExist
		}
		return fmt.Errorf("wechat business request failed: %s", errCode)
	}
	return nil
}
