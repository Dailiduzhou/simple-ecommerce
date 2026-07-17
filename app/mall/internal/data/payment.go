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
	response, _, err := a.client.QueryOrder(ctx, body)
	if err != nil {
		return nil, err
	}
	amount, err := strconv.ParseInt(response.TotalFee, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse wechat total fee: %w", err)
	}
	return &biz.PaymentQueryResult{Method: req.Method, OutTradeNo: response.OutTradeNo, TransactionID: response.TransactionId,
		TradeState: biz.ParseTradeState(response.TradeState), TradeStateDesc: response.TradeStateDesc,
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
	return &biz.PaymentCloseResult{Method: req.Method, OutTradeNo: req.OutTradeNo, Success: response.ReturnCode == "SUCCESS" && response.ResultCode == "SUCCESS"}, nil
}

func (a *WechatPaymentAdapter) ParseAndVerifyNotification(request *http.Request) (*biz.PaymentNotification, error) {
	if a.apiKey == "" {
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
	valid, err := wechat.VerifySign(a.apiKey, signType, notify)
	if err != nil || !valid {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_SIGNATURE_INVALID", "wechat notification signature is invalid")
	}
	amount, err := strconv.ParseInt(notify.TotalFee, 10, 64)
	if err != nil {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_AMOUNT_INVALID", "wechat total_fee is invalid")
	}
	return &biz.PaymentNotification{Provider: a.Provider(), ProviderEventID: notify.TransactionId, OutTradeNo: notify.OutTradeNo,
		TransactionID: notify.TransactionId, Amount: amount, Currency: biz.DefaultCurrency,
		PayloadHash: sha256Hex(body), VerifiedAt: time.Now().UTC()}, nil
}

type AlipayPaymentAdapter struct {
	client                    *alipayv3.ClientV3
	closeRequester            alipayCloseRequester
	notifyURL, publicCertPath string
	log                       *log.Helper
}

func NewAlipayPaymentAdapter(client *alipayv3.ClientV3, logger log.Logger) *AlipayPaymentAdapter {
	return &AlipayPaymentAdapter{client: client, closeRequester: &defaultAlipayCloseRequester{client: client}, notifyURL: notifyURLFromEnv("alipay"), log: log.NewHelper(logger)}
}
func newAlipayPaymentAdapterForTest(client *alipayv3.ClientV3, requester alipayCloseRequester, logger log.Logger) *AlipayPaymentAdapter {
	return &AlipayPaymentAdapter{client: client, closeRequester: requester, notifyURL: notifyURLFromEnv("alipay"), log: log.NewHelper(logger)}
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
	return biz.PaymentCapabilities{SupportsNotify: true, RequiresPoll: false, SupportsClose: true}
}
func (a *AlipayPaymentAdapter) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	if a.client == nil {
		return nil, paymentProviderNotConfigured("alipay")
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
	amount, err := yuanToFen(response.TotalAmount)
	if err != nil {
		return nil, err
	}
	state, description := mapAlipayTradeState(response.TradeStatus)
	return &biz.PaymentQueryResult{Method: req.Method, OutTradeNo: response.OutTradeNo, TransactionID: response.TradeNo,
		TradeState: state, TradeStateDesc: description, RawTradeState: response.TradeStatus, Amount: amount, Currency: biz.DefaultCurrency}, nil
}
func (a *AlipayPaymentAdapter) Close(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if a.client == nil || a.closeRequester == nil {
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
	var response alipayCloseRsp
	httpResponse, err := a.closeRequester.DoAliPayAPISelfV3(ctx, alipayv3.MethodPost, alipayTradeClosePath, body, &response)
	if err != nil {
		return nil, err
	}
	result := &biz.PaymentCloseResult{Method: req.Method, OutTradeNo: orEmpty(response.OutTradeNo, req.OutTradeNo), TransactionID: orEmpty(response.TradeNo, req.TransactionID), RawCode: response.Code, RawSubCode: response.SubCode}
	if httpResponse.StatusCode != http.StatusOK {
		return result, fmt.Errorf("alipay close returned HTTP %d", httpResponse.StatusCode)
	}
	if response.Code == alipaySuccessCode {
		result.Success = true
		return result, nil
	}
	if _, ok := alipaySubCodeAlreadyClosed[response.SubCode]; ok {
		result.Success = true
		return result, nil
	}
	return result, fmt.Errorf("alipay close rejected: %s", response.SubCode)
}
func (a *AlipayPaymentAdapter) ParseAndVerifyNotification(request *http.Request) (*biz.PaymentNotification, error) {
	if a.publicCertPath == "" {
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
	amount, err := yuanToFen(notify.GetString("total_amount"))
	if err != nil {
		return nil, errors.BadRequest("PAYMENT_NOTIFICATION_AMOUNT_INVALID", "alipay total_amount is invalid")
	}
	return &biz.PaymentNotification{Provider: a.Provider(), ProviderEventID: notify.GetString("notify_id"), OutTradeNo: notify.GetString("out_trade_no"),
		TransactionID: notify.GetString("trade_no"), Amount: amount, Currency: biz.DefaultCurrency,
		PayloadHash: sha256Hex(body), VerifiedAt: time.Now().UTC()}, nil
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
	log  *log.Helper
}

func NewPaymentRepo(data *Data, tx biz.TxManager, logger log.Logger) *PaymentRepo {
	return &PaymentRepo{data: data, tx: tx, log: log.NewHelper(logger)}
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
		if order.UserID != args.UserID || order.Status != biz.OrderStatusPendingPayment ||
			order.TotalAmountMinor != args.Amount || order.Currency != args.Currency {
			return biz.ErrPaymentConflict
		}
		row, err = q.CreatePaymentWithOutTradeNo(ctx, db.CreatePaymentWithOutTradeNoParams{
			OrderID: args.OrderID, UserID: args.UserID, MerchantID: args.MerchantID, AmountMinor: args.Amount,
			Currency: args.Currency, Status: biz.PaymentStatusCreating, PayChannel: args.Method,
			OutTradeNo: pgtype.Text{String: args.OutTradeNo, Valid: args.OutTradeNo != ""},
		})
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if stderrors.As(err, &pgErr) && pgErr.Code == "23505" {
			existing, getErr := r.GetActivePaymentByOrderMethod(ctx, args.OrderID, args.Method)
			if getErr == nil {
				return existing, nil
			}
			return nil, biz.ErrPaymentConflict
		}
		return nil, err
	}
	payment := toBizPayment(row)
	r.cachePayment(ctx, payment)
	return payment, nil
}

func (r *PaymentRepo) MarkPaymentPending(ctx context.Context, id int64, action biz.PaymentAction) (*biz.PaymentDO, error) {
	row, err := querierFromContext(ctx, r.data.q).MarkPaymentPending(ctx, db.MarkPaymentPendingParams{ID: id, ActionType: pgtype.Text{String: string(action.Type), Valid: action.Type != ""}, ActionPayload: action.Payload})
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			current, loadErr := r.GetPayment(ctx, id)
			if loadErr == nil && current.Status == biz.PaymentStatusPending {
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
func (r *PaymentRepo) GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*biz.PaymentDO, error) {
	return r.getPayment(ctx, redisKey("payment", "out_trade_no", outTradeNo), func() (db.Payment, error) {
		return querierFromContext(ctx, r.data.q).GetPaymentByOutTradeNo(ctx, pgtype.Text{String: outTradeNo, Valid: outTradeNo != ""})
	})
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
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		snapshot, err := q.GetPayment(ctx, args.PaymentID)
		if err != nil {
			return err
		}
		order, err := q.GetOrderForUpdate(ctx, snapshot.OrderID)
		if err != nil {
			return err
		}
		payment, err := q.GetPaymentForUpdate(ctx, args.PaymentID)
		if err != nil {
			return err
		}
		method, parseErr := biz.ParsePaymentMethod(payment.PayChannel)
		if parseErr != nil {
			return parseErr
		}
		if mismatch := validateProviderResult(payment, method, result); mismatch != "" {
			provider, fromStatus, event = method.Provider, payment.Status, "provider_mismatch"
			if mismatch == "amount mismatch" {
				observability.PaymentAmountMismatch(ctx, method.Provider)
			}
			observability.PaymentReconcileRequired(ctx, method.Provider)
			changed, err = q.MarkPaymentReconcileRequired(ctx, payment.ID)
			if err != nil && !stderrors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if recordErr := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, args.PollCount), LastError: mismatch}); recordErr != nil {
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
			if payment.Status != biz.PaymentStatusPending {
				observability.PaymentConflict(ctx, method.Provider)
				observability.PaymentReconcileRequired(ctx, method.Provider)
				changed, err = q.MarkPaymentReconcileRequired(ctx, payment.ID)
				if err != nil && !stderrors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, args.PollCount), LastError: "late success for payment state " + payment.Status}); err != nil {
					return err
				}
				return markNotificationProcessed(ctx, q, args.NotificationID)
			}
			changed, err = q.MarkPaymentSuccess(ctx, db.MarkPaymentSuccessParams{ID: payment.ID, ThirdPartyTxID: pgtype.Text{String: result.TransactionID, Valid: true}})
			if err != nil {
				return paymentStateAfterCAS(ctx, q, payment.ID, biz.PaymentStatusSuccess)
			}
			if _, err = q.MarkOrderPaid(ctx, payment.OrderID); err != nil {
				if !stderrors.Is(err, pgx.ErrNoRows) {
					return err
				}
				order, loadErr := q.GetOrder(ctx, payment.OrderID)
				if loadErr != nil {
					return loadErr
				}
				if order.Status != biz.OrderStatusPaid && order.Status != biz.OrderStatusShipped && order.Status != biz.OrderStatusCompleted {
					observability.PaymentConflict(ctx, method.Provider)
					observability.PaymentReconcileRequired(ctx, method.Provider)
					changed, err = q.MarkPaymentReconcileRequired(ctx, payment.ID)
					if err != nil && !stderrors.Is(err, pgx.ErrNoRows) {
						return err
					}
					if err := createReconciliationFailure(ctx, q, biz.ReconciliationFailure{PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, args.PollCount), LastError: "payment succeeded for order state " + order.Status}); err != nil {
						return err
					}
				}
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
			if order.Status == biz.OrderStatusPendingPayment {
				if _, err := q.MarkOrderCancelling(ctx, order.ID); err != nil {
					return err
				}
				if err := q.RestoreOrderItemStock(ctx, order.ID); err != nil {
					return err
				}
				if _, err := q.MarkOrderCancelled(ctx, order.ID); err != nil {
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
			return fmt.Errorf("payment state %s is not terminal", result.TradeState)
		}
		return markNotificationProcessed(ctx, q, args.NotificationID)
	})
	if err == nil && changed.ID > 0 {
		observability.PaymentTransition(ctx, fromStatus, changed.Status, event, provider)
		r.invalidatePayment(ctx, changed)
		r.invalidateOrder(ctx, changed.OrderID)
	}
	return err
}

func validateProviderResult(payment db.Payment, method biz.PaymentMethod, result *biz.PaymentQueryResult) string {
	if result == nil {
		return "empty provider result"
	}
	if result.OutTradeNo != payment.OutTradeNo.String {
		return "out_trade_no mismatch"
	}
	if result.Method.Normalize().String() != method.String() {
		return "payment method mismatch"
	}
	if result.Amount != payment.AmountMinor {
		return "amount mismatch"
	}
	if strings.ToUpper(result.Currency) != payment.Currency {
		return "currency mismatch"
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
func markNotificationProcessed(ctx context.Context, q db.Querier, id int64) error {
	if id <= 0 {
		return nil
	}
	return q.MarkPaymentNotificationProcessed(ctx, id)
}

func (r *PaymentRepo) MarkPayClosePending(ctx context.Context, args biz.CheckPayArgs) error {
	row, err := querierFromContext(ctx, r.data.q).MarkPaymentClosePending(ctx, args.PaymentID)
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return paymentStateAfterCAS(ctx, querierFromContext(ctx, r.data.q), args.PaymentID, biz.PaymentStatusClosePending)
		}
		return err
	}
	r.invalidatePayment(ctx, row)
	return nil
}
func (r *PaymentRepo) MarkReconciliationRequired(ctx context.Context, failure biz.ReconciliationFailure) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		row, err := q.MarkPaymentReconcileRequired(ctx, failure.PaymentID)
		if err != nil && !stderrors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if row.ID > 0 {
			r.invalidatePayment(ctx, row)
		}
		return createReconciliationFailure(ctx, q, failure)
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
	_, err := q.CreatePaymentReconciliationFailure(ctx, db.CreatePaymentReconciliationFailureParams{PaymentID: failure.PaymentID, Provider: failure.Provider, RiverJobID: jobID, Attempt: int32(max(1, failure.Attempt)), LastError: failure.LastError})
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
	if payment.OutTradeNo.Valid {
		r.deleteCache(ctx, redisKey("payment", "out_trade_no", payment.OutTradeNo.String))
	}
}
func (r *PaymentRepo) invalidateOrder(ctx context.Context, orderID int64) {
	order, err := querierFromContext(ctx, r.data.q).GetOrder(ctx, orderID)
	if err != nil {
		return
	}
	r.deleteCache(ctx, redisKey("order", orderID))
	r.deleteCache(ctx, redisKey("order", "user", orderID, order.UserID))
	if order.OutTradeNo.Valid {
		r.deleteCache(ctx, redisKey("order", "no", order.OutTradeNo.String))
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
	payment := &biz.PaymentDO{ID: row.ID, OrderID: row.OrderID, UserID: row.UserID, MerchantID: row.MerchantID, Amount: row.AmountMinor, Currency: row.Currency, Status: row.Status, Method: row.PayChannel, OutTradeNo: row.OutTradeNo.String, ThirdPartyTxID: row.ThirdPartyTxID.String, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if row.ActionType.Valid {
		payment.Action.Type = biz.PaymentActionType(row.ActionType.String)
		payment.Action.Payload = append(json.RawMessage(nil), row.ActionPayload...)
	}
	if row.PaidAt.Valid {
		paidAt := row.PaidAt.Time
		payment.PaidAt = &paidAt
	}
	return payment
}

type PaymentMQRepo struct {
	client *river.Client[pgx.Tx]
	log    *log.Helper
}

func NewPaymentMQRepo(client *river.Client[pgx.Tx], logger log.Logger) *PaymentMQRepo {
	return &PaymentMQRepo{client: client, log: log.NewHelper(logger)}
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
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infow("msg", "deduplicated active reconciliation job", "job_id", result.Job.ID, "payment_id", args.PaymentID)
	}
	return toBizMQJob(result.Job), nil
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
			return nil
		}
		if err != nil {
			return err
		}
		args.NotificationID = row.ID
		_, err = r.jobs.EnqueueCheckPayTx(ctx, args, time.Time{})
		return err
	})
	return duplicate, err
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
