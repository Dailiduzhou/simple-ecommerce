package data

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/shopspring/decimal"
)

// alipayCodeTradeNotExist:查询接口的"交易不存在"错误码,
// 代表渠道侧从未建单或订单已被清理。
const alipayCodeTradeNotExist = "ACQ.TRADE_NOT_EXIST"

// alipayCodeAlreadyClosed 是 gopay v3 ErrResponse.Code 中可幂等视为已关单的终态码。
var alipayCodeAlreadyClosed = map[string]struct{}{
	"ACQ.TRADE_ALREADY_CLOSED": {},
	"ACQ.REASON_TRADE_CLOSED":  {},
}

// alipayTradeRequester keeps the adapter testable while production delegates
// directly to gopay's built-in v3 trade methods.
type alipayTradeRequester interface {
	TradeClose(context.Context, gopay.BodyMap) (*alipayv3.TradeCloseRsp, error)
	TradeRefund(context.Context, gopay.BodyMap) (*alipayv3.TradeRefundRsp, error)
}

func alipayDefinitiveRefundRejection(code string) bool {
	switch code {
	case "ACQ.TRADE_NOT_EXIST", "ACQ.REFUND_AMT_NOT_EQUAL_TOTAL", "ACQ.REASON_TRADE_REFUND_FEE_ERR", "ACQ.TRADE_HAS_FINISHED":
		return true
	default:
		return false
	}
}

// Account/environment binding is persisted with locally signed actions so a
// later configuration switch cannot turn a paid trade into "not exist".
func (a *AlipayPaymentAdapter) providerAccount() string {
	return fmt.Sprintf("%s:%t:%s", a.client.AppId, a.client.IsProd, sha256Hex([]byte(a.client.AppAuthToken)))
}

type AlipayPaymentAdapter struct {
	client                    *alipayv3.ClientV3
	wapSigner                 *alipay.Client // v2 local signer; v3 WAP helper overrides product_code incorrectly
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
	// time_expire bounds BOTH initial invocation and payment, unlike timeout_express:
	// https://opendocs.alipay.com/support/01rfuw
	// Alipay interprets it in China Standard Time, at second precision.
	deadline := req.ExpiresAt.Truncate(time.Second)
	// Payability was checked under the order lock using PostgreSQL's clock.
	// Preserve its absolute deadline; local clock skew must not shorten it.
	if deadline.IsZero() {
		return nil, biz.ErrOrderExpired
	}
	body := make(gopay.BodyMap)
	body.Set("time_expire", deadline.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05"))
	body.Set("subject", req.Description).Set("out_trade_no", req.OutTradeNo).Set("total_amount", fenToYuan(req.Amount))
	if a.notifyURL != "" {
		body.Set("notify_url", a.notifyURL)
	}
	var payload string
	var actionType biz.PaymentActionType
	var err error
	switch req.Method.Product {
	case "wap":
		if a.wapSigner == nil {
			return nil, paymentProviderNotConfigured("alipay wap signer")
		}
		payload, err = a.wapSigner.TradeWapPay(ctx, body)
		actionType = biz.PaymentActionRedirect
	case "app":
		body.Set("product_code", "QUICK_MSECURITY_PAY")
		payload, err = a.client.TradeAppPay(ctx, body)
		actionType = biz.PaymentActionInvoke
	default:
		return nil, biz.ErrPaymentProviderUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &biz.PaymentPrepayResult{Action: paymentAction(actionType, biz.SignedPaymentPayload{Payload: payload, ExpiresAt: deadline.UTC(), ProviderAccount: a.providerAccount()})}, nil
}

func (a *AlipayPaymentAdapter) Query(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("alipay")
	}
	if req.ExpectedProviderAccount != "" && req.ExpectedProviderAccount != a.providerAccount() {
		return nil, fmt.Errorf("alipay query account or environment differs from signed payment parameters")
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
	if response.ErrResponse.Code == "ACQ.TRADE_STATUS_ERROR" {
		return result, biz.ErrProviderTradeStateConflict
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
			// Only known business rejections are definitive. Throttling, signature
			// failures and unknown/system errors must remain reconcilable.
			Rejection: err == nil && response.StatusCode == http.StatusBadRequest && alipayDefinitiveRefundRejection(response.ErrResponse.Code),
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
