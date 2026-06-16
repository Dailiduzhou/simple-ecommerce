package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	gopaywechat "github.com/go-pay/gopay/wechat"
	"github.com/go-pay/util"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/shopspring/decimal"
)

// EnvAlipayNotifyURL is the env var consulted for the payment-channel
// async-notify URL (the `notify_url` sent to alipay and wechat).
//
// 对应 .env.example 中的 ALIPAY_NOTIFY_URL 形如
//
//	http://domain.example/v1/pay/alipay/notify
//
// 服务端需要把 v1/pay/wechat/notify 同样挂在同一域名下(alipay/wechat 共用
// 同一通知域名,通过路径区分渠道),所以两个适配器都从同一个 env 变量读取。
//
// notify_url 是服务端配置,不能接受来自请求方(前端/RPC)的覆盖 — 第三方支付
// 平台会按此 URL 回跳,如果被任意指定,攻击者可以把它指向自己控制的地址
// 接收支付结果,导致订单状态被外部篡改。
const EnvAlipayNotifyURL = "ALIPAY_NOTIFY_URL"

// notifyURLFromEnv 读取 EnvAlipayNotifyURL;为空时返回空串,由适配器决定
// 是否透传给三方(空值意味着不发送 notify_url 字段)。
func notifyURLFromEnv() string {
	return os.Getenv(EnvAlipayNotifyURL)
}

var (
	_ biz.PaymentAdapter  = (*WechatPaymentAdapter)(nil)
	_ biz.PaymentAdapter  = (*AlipayPaymentAdapter)(nil)
	_ biz.PaymentRepo     = (*PaymentRepo)(nil)
	_ biz.PaymentMQRepo   = (*PaymentMQRepo)(nil)
	_ biz.PaymentSyncRepo = (*PaymentSyncRepo)(nil)
)

type WechatPaymentAdapter struct {
	client    *gopaywechat.Client
	notifyURL string
	log       *log.Helper
}

func NewWechatPaymentAdapter(c *conf.Payment, logger log.Logger) *WechatPaymentAdapter {
	var client *gopaywechat.Client
	if c != nil && c.Wechat != nil && c.Wechat.ApiKey != "" {
		client = gopaywechat.NewClient(c.Wechat.AppId, c.Wechat.MchId, c.Wechat.ApiKey, c.Wechat.IsProduction)
	}
	return &WechatPaymentAdapter{
		client:    client,
		notifyURL: notifyURLFromEnv(),
		log:       log.NewHelper(logger),
	}
}

func (a *WechatPaymentAdapter) Channel() string {
	return string(biz.Wechat)
}

func (a *WechatPaymentAdapter) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warn("wechat payment adapter is not configured")
		return nil, wechatPayNotConfigured()
	}

	nonceStr := util.RandomString(32)
	signType := gopaywechat.SignType_MD5
	body := make(gopay.BodyMap)
	body.Set("nonce_str", nonceStr).
		Set("body", req.Description).
		Set("out_trade_no", req.OutTradeNo).
		Set("total_fee", req.TotalAmount).
		Set("spbill_create_ip", "127.0.0.1").
		Set("trade_type", gopaywechat.TradeType_JsApi).
		Set("sign_type", signType).
		Set("openid", req.OpenID).
		Set("notify_url", a.notifyURL)

	wxRsp, err := a.client.UnifiedOrder(ctx, body)
	if err != nil {
		return nil, err
	}

	timeStamp := strconv.FormatInt(time.Now().Unix(), 10)
	pkg := "prepay_id=" + wxRsp.PrepayId
	return &biz.PaymentPrepayResult{
		Channel:    string(biz.Wechat),
		OutTradeNo: req.OutTradeNo,
		PrepayID:   wxRsp.PrepayId,
		AppID:      a.client.AppId,
		TimeStamp:  timeStamp,
		NonceStr:   wxRsp.NonceStr,
		Package:    pkg,
		SignType:   signType,
		PaySign:    gopaywechat.GetJsapiPaySign(a.client.AppId, wxRsp.NonceStr, pkg, signType, timeStamp, a.client.ApiKey),
		CodeURL:    wxRsp.CodeUrl,
	}, nil
}

func (a *WechatPaymentAdapter) QueryOrder(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warnf("wechat payment adapter is not configured, out_trade_no=%s", req.OutTradeNo)
		return nil, wechatPayNotConfigured()
	}

	body := make(gopay.BodyMap)
	body.Set("nonce_str", util.RandomString(32)).
		Set("sign_type", gopaywechat.SignType_MD5)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("transaction_id", req.TransactionID)
	}

	wxRsp, _, err := a.client.QueryOrder(ctx, body)
	if err != nil {
		return nil, err
	}
	totalAmount, _ := strconv.Atoi(wxRsp.TotalFee)
	return &biz.PaymentQueryResult{
		Channel:        string(biz.Wechat),
		OutTradeNo:     wxRsp.OutTradeNo,
		TransactionID:  wxRsp.TransactionId,
		TradeState:     biz.ParseTradeState(wxRsp.TradeState),
		TradeStateDesc: wxRsp.TradeStateDesc,
		RawTradeState:  wxRsp.TradeState,
		TotalAmount:    int32(totalAmount),
	}, nil
}

func (a *WechatPaymentAdapter) CloseOrder(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warnf("wechat payment adapter is not configured, out_trade_no=%s", req.OutTradeNo)
		return nil, wechatPayNotConfigured()
	}

	body := make(gopay.BodyMap)
	body.Set("nonce_str", util.RandomString(32)).
		Set("out_trade_no", req.OutTradeNo)
	wxRsp, err := a.client.CloseOrder(ctx, body)
	if err != nil {
		return nil, err
	}

	return &biz.PaymentCloseResult{
		Channel:    string(biz.Wechat),
		OutTradeNo: req.OutTradeNo,
		Success:    wxRsp.ReturnCode == "SUCCESS" && wxRsp.ResultCode == "SUCCESS",
	}, nil
}

type AlipayPaymentAdapter struct {
	client         *alipayv3.ClientV3
	closeRequester alipayCloseRequester
	notifyURL      string
	log            *log.Helper
}

func NewAlipayPaymentAdapter(client *alipayv3.ClientV3, logger log.Logger) *AlipayPaymentAdapter {
	return &AlipayPaymentAdapter{
		client:         client,
		closeRequester: &defaultAlipayCloseRequester{client: client},
		notifyURL:      notifyURLFromEnv(),
		log:            log.NewHelper(logger),
	}
}

// newAlipayPaymentAdapterForTest 仅供测试用,允许注入自定义 closeRequester。
// 命名以下划线开头以暗示包外不直接使用;实际测试放在同 package 可直接调。
func newAlipayPaymentAdapterForTest(client *alipayv3.ClientV3, cr alipayCloseRequester, logger log.Logger) *AlipayPaymentAdapter {
	return &AlipayPaymentAdapter{
		client:         client,
		closeRequester: cr,
		notifyURL:      notifyURLFromEnv(),
		log:            log.NewHelper(logger),
	}
}

func (a *AlipayPaymentAdapter) Channel() string {
	return string(biz.Alipay)
}

func (a *AlipayPaymentAdapter) Prepay(ctx context.Context, req biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warn("alipay payment adapter is not configured")
		return nil, alipayNotConfigured()
	}

	body := make(gopay.BodyMap)
	body.Set("subject", req.Description).
		Set("out_trade_no", req.OutTradeNo).
		Set("total_amount", fenToYuan(req.TotalAmount))
	if a.notifyURL != "" {
		body.Set("notify_url", a.notifyURL)
	}

	// 统一走手机网站支付(WAP),返回跳转 URL;service 层在 WAP/APP 两个
	// 子渠道上都是用 URL_REDIRECT + code_url 消费,这里把 URL 放进 CodeURL
	// 字段统一返回。TradeWapPay 内部会自动加 product_code=FAST_INSTANT_TRADE_PAY。
	payURL, err := a.client.TradeWapPay(ctx, body)
	if err != nil {
		return nil, err
	}

	return &biz.PaymentPrepayResult{
		Channel:    string(biz.Alipay),
		OutTradeNo: req.OutTradeNo,
		PayURL:     payURL,
	}, nil
}

func (a *AlipayPaymentAdapter) QueryOrder(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warnf("alipay payment adapter is not configured, out_trade_no=%s", req.OutTradeNo)
		return nil, alipayNotConfigured()
	}
	if req.OutTradeNo == "" && req.TransactionID == "" {
		return nil, alipayOrderIDRequired()
	}

	body := make(gopay.BodyMap)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("trade_no", req.TransactionID)
	}

	aliRsp, err := a.client.TradeQuery(ctx, body)
	if err != nil {
		return nil, err
	}

	totalAmount, _ := yuanToFen(aliRsp.TotalAmount)
	state, stateDesc := mapAlipayTradeState(aliRsp.TradeStatus)
	return &biz.PaymentQueryResult{
		Channel:        string(biz.Alipay),
		OutTradeNo:     aliRsp.OutTradeNo,
		TransactionID:  aliRsp.TradeNo,
		TradeState:     state,
		TradeStateDesc: stateDesc,
		RawTradeState:  aliRsp.TradeStatus,
		TotalAmount:    totalAmount,
	}, nil
}

func (a *AlipayPaymentAdapter) CloseOrder(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warnf("alipay payment adapter is not configured, out_trade_no=%s", req.OutTradeNo)
		return nil, alipayNotConfigured()
	}
	if req.OutTradeNo == "" && req.TransactionID == "" {
		return nil, alipayOrderIDRequired()
	}

	body := make(gopay.BodyMap)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("trade_no", req.TransactionID)
	}

	var rsp alipayCloseRsp
	res, err := a.closeRequester.DoAliPayAPISelfV3(ctx, alipayv3.MethodPost, alipayTradeClosePath, body, &rsp)
	if err != nil {
		a.log.WithContext(ctx).Errorf("alipay trade close transport error out_trade_no=%s err=%v",
			req.OutTradeNo, err)
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		a.log.WithContext(ctx).Errorf("alipay trade close http %d out_trade_no=%s code=%s msg=%s",
			res.StatusCode, req.OutTradeNo, rsp.Code, rsp.ErrResponse.Message)
		return &biz.PaymentCloseResult{
			Channel:    string(biz.Alipay),
			OutTradeNo: req.OutTradeNo,
			RawCode:    rsp.Code,
		}, fmt.Errorf("alipay trade close http %d: %s", res.StatusCode, rsp.ErrResponse.Message)
	}

	if rsp.Code == alipaySuccessCode {
		return &biz.PaymentCloseResult{
			Channel:       string(biz.Alipay),
			OutTradeNo:    rsp.OutTradeNo,
			TransactionID: rsp.TradeNo,
			Success:       true,
			RawCode:       rsp.Code,
		}, nil
	}

	if _, ok := alipaySubCodeAlreadyClosed[rsp.SubCode]; ok {
		a.log.WithContext(ctx).Infof("alipay trade close idempotent success sub_code=%s out_trade_no=%s",
			rsp.SubCode, req.OutTradeNo)
		return &biz.PaymentCloseResult{
			Channel:       string(biz.Alipay),
			OutTradeNo:    orEmpty(rsp.OutTradeNo, req.OutTradeNo),
			TransactionID: orEmpty(rsp.TradeNo, req.TransactionID),
			Success:       true,
			RawCode:       rsp.Code,
			RawSubCode:    rsp.SubCode,
		}, nil
	}

	a.log.WithContext(ctx).Errorf("alipay trade close rejected out_trade_no=%s code=%s sub_code=%s msg=%s",
		req.OutTradeNo, rsp.Code, rsp.SubCode, rsp.SubMsg)
	return &biz.PaymentCloseResult{
		Channel:    string(biz.Alipay),
		OutTradeNo: req.OutTradeNo,
		Success:    false,
		RawCode:    rsp.Code,
		RawSubCode: rsp.SubCode,
	}, fmt.Errorf("alipay trade close rejected: sub_code=%s", rsp.SubCode)
}

func orEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type PaymentRepo struct {
	pool *pgxpool.Pool
	q    db.Querier
}

func NewPaymentRepo(pool *pgxpool.Pool) *PaymentRepo {
	return &PaymentRepo{pool: pool, q: db.New(pool)}
}

func (r *PaymentRepo) CreatePayment(ctx context.Context, args biz.CreatePaymentArgs) (*biz.PaymentDO, error) {
	amount := decimal.NewFromInt(int64(args.Amount))
	payment, err := querierFromContext(ctx, r.q).CreatePaymentWithOutTradeNo(ctx, db.CreatePaymentWithOutTradeNoParams{
		OrderID:    args.OrderID,
		UserID:     args.UserID,
		MerchantID: args.MerchantID,
		Amount:     amount,
		Status:     "pending",
		PayChannel: args.PayChannel,
		OutTradeNo: pgtype.Text{String: args.OutTradeNo, Valid: args.OutTradeNo != ""},
	})
	if err != nil {
		// 23505 on idx_payments_active_out_trade_no_channel → concurrent
		// insert raced and won; surface as a domain conflict.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, biz.ErrPaymentConflict
		}
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func (r *PaymentRepo) GetPayment(ctx context.Context, id int64) (*biz.PaymentDO, error) {
	payment, err := querierFromContext(ctx, r.q).GetPayment(ctx, id)
	if err != nil {
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func (r *PaymentRepo) GetPaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	payment, err := querierFromContext(ctx, r.q).GetPaymentByOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func (r *PaymentRepo) GetActivePaymentByOrderChannel(ctx context.Context, orderID int64, channel string) (*biz.PaymentDO, error) {
	payment, err := querierFromContext(ctx, r.q).GetActivePaymentByOrderChannel(ctx, db.GetActivePaymentByOrderChannelParams{
		OrderID:    orderID,
		PayChannel: channel,
	})
	if err != nil {
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func (r *PaymentRepo) GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*biz.PaymentDO, error) {
	payment, err := querierFromContext(ctx, r.q).GetPaymentByOutTradeNo(ctx, pgtype.Text{String: outTradeNo, Valid: outTradeNo != ""})
	if err != nil {
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func toBizPaymentDO(p db.Payment) *biz.PaymentDO {
	d := &biz.PaymentDO{
		ID:             p.ID,
		OrderID:        p.OrderID,
		UserID:         p.UserID,
		MerchantID:     p.MerchantID,
		Amount:         int32(p.Amount.IntPart()),
		Status:         p.Status,
		PayChannel:     p.PayChannel,
		OutTradeNo:     p.OutTradeNo.String,
		ThirdPartyTxID: p.ThirdPartyTxID.String,
		CreatedAt:      p.CreatedAt.Time,
		UpdatedAt:      p.UpdatedAt.Time,
	}
	if p.PaidAt.Valid {
		t := p.PaidAt.Time
		d.PaidAt = &t
	}
	return d
}

func wechatPayNotConfigured() error {
	return errors.ServiceUnavailable("WECHAT_PAY_NOT_CONFIGURED", "wechat pay adapter is not configured")
}

func alipayNotConfigured() error {
	return errors.ServiceUnavailable("ALIPAY_NOT_CONFIGURED", "alipay adapter is not configured")
}

// alipayOrderIDRequired 用于 Query/Close 这类需要定位一笔已有交易的请求。
// 支付宝 openapi 在这两类接口上只接受 out_trade_no(商户订单号)和 trade_no
// (支付宝交易号)二选一,都不传会被底层库直接拒掉;这里前置成本项目的 kratos
// 错误,既给出稳定 reason 码,也避免把 v3 库的英文 raw error 漏到上游。
func alipayOrderIDRequired() error {
	return errors.BadRequest("ALIPAY_ORDER_ID_REQUIRED",
		"alipay query/close requires out_trade_no or trade_no (merchant order number or alipay transaction number)")
}

type PaymentMQRepo struct {
	client *river.Client[pgx.Tx]
	log    *log.Helper
}

func NewPaymentMQRepo(client *river.Client[pgx.Tx], logger log.Logger) *PaymentMQRepo {
	return &PaymentMQRepo{client: client, log: log.NewHelper(logger)}
}

func (r *PaymentMQRepo) EnqueueCheckPay(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	opts := r.checkPayInsertOpts(args, scheduledAt)
	result, err := r.client.Insert(ctx, args, opts)
	if err != nil {
		return nil, err
	}
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infof("skipped duplicate river job kind=%s job_id=%d", args.Kind(), result.Job.ID)
	}
	return toBizMQJob(result.Job), nil
}

func (r *PaymentMQRepo) EnqueueCheckPayTx(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("EnqueueCheckPayTx requires a transaction in context")
	}
	opts := r.checkPayInsertOpts(args, scheduledAt)
	result, err := r.client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return nil, err
	}
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infof("skipped duplicate river tx job kind=%s job_id=%d", args.Kind(), result.Job.ID)
	}
	return toBizMQJob(result.Job), nil
}

func (r *PaymentMQRepo) checkPayInsertOpts(args biz.CheckPayArgs, scheduledAt time.Time) *river.InsertOpts {
	opts := &river.InsertOpts{
		MaxAttempts: args.MaxPolls,
		Queue:       "payments",
		Tags: []string{
			fmt.Sprintf("pay-channel-%s", args.Channel),
			fmt.Sprintf("payment-%d", args.PaymentID),
		},
		// Uniqueness is enforced by kind + queue + OutTradeNo only, because
		// CheckPayArgs.OutTradeNo carries the `river:"unique"` struct tag.
		// Re-enqueues with different MaxPolls/PollIntervalSeconds for the
		// same OutTradeNo are therefore deduplicated. Duplicate writes are
		// also guarded by idx_payments_active_out_trade_no_channel and
		// idx_payments_third_party_tx_id_channel (migration 000007).
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
		},
	}
	if !scheduledAt.IsZero() {
		opts.ScheduledAt = scheduledAt
	}
	return opts
}

func (r *PaymentMQRepo) GetMQJob(ctx context.Context, jobID int64) (*biz.MQJob, error) {
	job, err := r.client.JobGet(ctx, jobID)
	if err != nil {
		if stderrors.Is(err, rivertype.ErrNotFound) {
			return nil, errors.NotFound("MQ_JOB_NOT_FOUND", "mq job not found")
		}
		return nil, err
	}
	return toBizMQJob(job), nil
}

type PaymentSyncRepo struct {
	tx biz.TxManager
}

func NewPaymentSyncRepo(tx biz.TxManager) *PaymentSyncRepo {
	return &PaymentSyncRepo{tx: tx}
}

func (r *PaymentSyncRepo) ApplyPayQuery(ctx context.Context, args biz.CheckPayArgs, result *biz.PaymentQueryResult) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)

		switch result.TradeState {
		case biz.TradeStateSuccess:
			orderID, err := r.applySuccess(ctx, q, args, result.TransactionID)
			if err != nil {
				return err
			}
			return finalizeOrder(ctx, q, orderID, true)
		case biz.TradeStateRefund:
			return r.applyRefund(ctx, q, args)
		case biz.TradeStateClosed, biz.TradeStateRevoked, biz.TradeStatePayError:
			orderID, err := r.applyFailed(ctx, q, args)
			if err != nil {
				return err
			}
			return finalizeOrder(ctx, q, orderID, false)
		default:
			return fmt.Errorf("wechat pay state %s is not terminal", result.TradeState.String())
		}
	})
}

func (r *PaymentSyncRepo) MarkPayExpired(ctx context.Context, args biz.CheckPayArgs) error {
	return r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		orderID, err := r.applyFailed(ctx, q, args)
		if err != nil {
			return err
		}
		return finalizeOrder(ctx, q, orderID, false)
	})
}

func (r *PaymentSyncRepo) applySuccess(ctx context.Context, q db.Querier, args biz.CheckPayArgs, txID string) (int64, error) {
	payment, err := q.UpdatePaymentSuccess(ctx, db.UpdatePaymentSuccessParams{
		ID: args.PaymentID,
		ThirdPartyTxID: pgtype.Text{
			String: txID,
			Valid:  txID != "",
		},
	})
	if err != nil {
		return 0, err
	}
	orderID := args.OrderID
	if orderID <= 0 {
		orderID = payment.OrderID
	}
	return orderID, nil
}

func (r *PaymentSyncRepo) applyRefund(ctx context.Context, q db.Querier, args biz.CheckPayArgs) error {
	return q.UpdatePaymentRefunded(ctx, args.PaymentID)
}

func (r *PaymentSyncRepo) applyFailed(ctx context.Context, q db.Querier, args biz.CheckPayArgs) (int64, error) {
	if err := q.UpdatePaymentFailed(ctx, args.PaymentID); err != nil {
		return 0, err
	}
	orderID := args.OrderID
	if orderID <= 0 {
		payment, err := q.GetPayment(ctx, args.PaymentID)
		if err != nil {
			return 0, err
		}
		orderID = payment.OrderID
	}
	return orderID, nil
}

func finalizeOrder(ctx context.Context, q db.Querier, orderID int64, success bool) error {
	if orderID <= 0 {
		return nil
	}
	if success {
		return q.CompleteOrder(ctx, orderID)
	}
	return q.CancelOrder(ctx, orderID)
}

func toBizMQJob(row *rivertype.JobRow) *biz.MQJob {
	if row == nil {
		return nil
	}
	errors := make([]biz.MQJobError, len(row.Errors))
	for i, err := range row.Errors {
		errors[i] = biz.MQJobError{
			Attempt: err.Attempt,
			Error:   err.Error,
			At:      err.At,
		}
	}
	return &biz.MQJob{
		ID:          row.ID,
		Kind:        row.Kind,
		Queue:       row.Queue,
		State:       string(row.State),
		Attempt:     row.Attempt,
		MaxAttempts: row.MaxAttempts,
		ArgsJSON:    string(row.EncodedArgs),
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		ScheduledAt: row.ScheduledAt,
		AttemptedAt: row.AttemptedAt,
		FinalizedAt: row.FinalizedAt,
		Errors:      errors,
	}
}

func fenToYuan(amount int32) string {
	return strconv.FormatFloat(float64(amount)/100, 'f', 2, 64)
}

func yuanToFen(amount string) (int32, error) {
	value, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return 0, err
	}
	return int32(value * 100), nil
}

// mapAlipayTradeState 把支付宝查单响应的 trade_status 字符串映射到内部
// biz.TradeState。支付宝 openapi 文档定义的 4 个终态/中间态:
//
//	WAIT_BUYER_PAY  交易创建,等待买家付款
//	TRADE_CLOSED    未付款交易超时关闭,或支付完成后全额退款
//	TRADE_SUCCESS   交易支付成功(可退款)
//	TRADE_FINISHED  交易结束,不可退款
//
// TRADE_FINISHED 与 TRADE_SUCCESS 都归到 TradeStateSuccess:对于商户来
// 说"已付"是同一个语义,退款窗口的差异不体现在我们当前的 biz 状态机
// 里(TradeStateRefund 已经覆盖了"已退款"这一终态)。
func mapAlipayTradeState(status string) (biz.TradeState, string) {
	switch status {
	case "WAIT_BUYER_PAY":
		return biz.TradeStateNotPay, status
	case "TRADE_CLOSED":
		return biz.TradeStateClosed, status
	case "TRADE_SUCCESS":
		return biz.TradeStateSuccess, status
	case "TRADE_FINISHED":
		return biz.TradeStateSuccess, status
	default:
		return biz.TradeStateUnspecified, status
	}
}
