package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	gopayalipay "github.com/go-pay/gopay/alipay"
	gopaywechat "github.com/go-pay/gopay/wechat"
	"github.com/go-pay/util"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/shopspring/decimal"
)

var (
	_ biz.PaymentAdapter  = (*WechatPaymentAdapter)(nil)
	_ biz.PaymentAdapter  = (*AlipayPaymentAdapter)(nil)
	_ biz.PaymentRepo     = (*PaymentRepo)(nil)
	_ biz.PaymentMQRepo   = (*PaymentMQRepo)(nil)
	_ biz.PaymentSyncRepo = (*PaymentSyncRepo)(nil)
)

type WechatPaymentAdapter struct {
	client *gopaywechat.Client
	log    *log.Helper
}

func NewWechatPaymentAdapter(logger log.Logger) *WechatPaymentAdapter {
	return &WechatPaymentAdapter{log: log.NewHelper(logger)}
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
		Set("notify_url", req.NotifyURL).
		Set("trade_type", gopaywechat.TradeType_JsApi).
		Set("sign_type", signType).
		Set("openid", req.OpenID)

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
	client *gopayalipay.Client
	log    *log.Helper
}

func NewAlipayPaymentAdapter(logger log.Logger) *AlipayPaymentAdapter {
	return &AlipayPaymentAdapter{log: log.NewHelper(logger)}
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
	if req.NotifyURL != "" {
		body.Set("notify_url", req.NotifyURL)
	}

	aliRsp, err := a.client.TradePrecreate(ctx, body)
	if err != nil {
		return nil, err
	}

	return &biz.PaymentPrepayResult{
		Channel:    string(biz.Alipay),
		OutTradeNo: req.OutTradeNo,
		CodeURL:    aliRsp.Response.QrCode,
	}, nil
}

func (a *AlipayPaymentAdapter) QueryOrder(ctx context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warnf("alipay payment adapter is not configured, out_trade_no=%s", req.OutTradeNo)
		return nil, alipayNotConfigured()
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

	totalAmount, _ := yuanToFen(aliRsp.Response.TotalAmount)
	state, stateDesc := mapAlipayTradeState(aliRsp.Response.TradeStatus)
	return &biz.PaymentQueryResult{
		Channel:        string(biz.Alipay),
		OutTradeNo:     aliRsp.Response.OutTradeNo,
		TransactionID:  aliRsp.Response.TradeNo,
		TradeState:     state,
		TradeStateDesc: stateDesc,
		RawTradeState:  aliRsp.Response.TradeStatus,
		TotalAmount:    totalAmount,
	}, nil
}

func (a *AlipayPaymentAdapter) CloseOrder(ctx context.Context, req biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	if a.client == nil {
		a.log.WithContext(ctx).Warnf("alipay payment adapter is not configured, out_trade_no=%s", req.OutTradeNo)
		return nil, alipayNotConfigured()
	}

	body := make(gopay.BodyMap)
	if req.OutTradeNo != "" {
		body.Set("out_trade_no", req.OutTradeNo)
	}
	if req.TransactionID != "" {
		body.Set("trade_no", req.TransactionID)
	}

	aliRsp, err := a.client.TradeClose(ctx, body)
	if err != nil {
		return nil, err
	}

	return &biz.PaymentCloseResult{
		Channel:       string(biz.Alipay),
		OutTradeNo:    aliRsp.Response.OutTradeNo,
		TransactionID: aliRsp.Response.TradeNo,
		Success:       aliRsp.Response != nil,
	}, nil
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
	payment, err := r.q.CreatePayment(ctx, db.CreatePaymentParams{
		OrderID:    args.OrderID,
		UserID:     args.UserID,
		MerchantID: args.MerchantID,
		Amount:     amount,
		Status:     "pending",
		PayChannel: args.PayChannel,
	})
	if err != nil {
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func (r *PaymentRepo) GetPayment(ctx context.Context, id int64) (*biz.PaymentDO, error) {
	payment, err := r.q.GetPayment(ctx, id)
	if err != nil {
		return nil, err
	}
	return toBizPaymentDO(payment), nil
}

func (r *PaymentRepo) GetPaymentByOrder(ctx context.Context, orderID int64) (*biz.PaymentDO, error) {
	payment, err := r.q.GetPaymentByOrder(ctx, orderID)
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

type PaymentMQRepo struct {
	client *river.Client[pgx.Tx]
	log    *log.Helper
}

func NewPaymentMQRepo(client *river.Client[pgx.Tx], logger log.Logger) *PaymentMQRepo {
	return &PaymentMQRepo{client: client, log: log.NewHelper(logger)}
}

func (r *PaymentMQRepo) EnqueueCheckPay(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	opts := &river.InsertOpts{
		MaxAttempts: args.MaxPolls,
		Queue:       "payments",
		Tags: []string{
			fmt.Sprintf("pay-channel-%s", args.Channel),
			fmt.Sprintf("payment-%d", args.PaymentID),
		},
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
		},
	}
	if !scheduledAt.IsZero() {
		opts.ScheduledAt = scheduledAt
	}

	result, err := r.client.Insert(ctx, args, opts)
	if err != nil {
		return nil, err
	}
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infof("skipped duplicate river job kind=%s job_id=%d", args.Kind(), result.Job.ID)
	}
	return toBizMQJob(result.Job), nil
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
	pool *pgxpool.Pool
}

func NewPaymentSyncRepo(pool *pgxpool.Pool) *PaymentSyncRepo {
	return &PaymentSyncRepo{pool: pool}
}

func (r *PaymentSyncRepo) ApplyPayQuery(ctx context.Context, args biz.CheckPayArgs, result *biz.PaymentQueryResult) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := db.New(tx)
	orderID := args.OrderID

	switch result.TradeState {
	case biz.TradeStateSuccess:
		payment, err := q.UpdatePaymentSuccess(ctx, db.UpdatePaymentSuccessParams{
			ID: args.PaymentID,
			ThirdPartyTxID: pgtype.Text{
				String: result.TransactionID,
				Valid:  result.TransactionID != "",
			},
		})
		if err != nil {
			return err
		}
		if orderID <= 0 {
			orderID = payment.OrderID
		}
		if orderID > 0 {
			if err := q.CompleteOrder(ctx, orderID); err != nil {
				return err
			}
		}
	case biz.TradeStateRefund:
		if err := q.UpdatePaymentRefunded(ctx, args.PaymentID); err != nil {
			return err
		}
	case biz.TradeStateClosed, biz.TradeStateRevoked, biz.TradeStatePayError:
		if err := q.UpdatePaymentFailed(ctx, args.PaymentID); err != nil {
			return err
		}
		if orderID <= 0 {
			payment, err := q.GetPayment(ctx, args.PaymentID)
			if err != nil {
				return err
			}
			orderID = payment.OrderID
		}
		if orderID > 0 {
			if err := q.CancelOrder(ctx, orderID); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("wechat pay state %s is not terminal", result.TradeState.String())
	}

	return tx.Commit(ctx)
}

func (r *PaymentSyncRepo) MarkPayExpired(ctx context.Context, args biz.CheckPayArgs) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := db.New(tx)
	if err := q.UpdatePaymentFailed(ctx, args.PaymentID); err != nil {
		return err
	}

	orderID := args.OrderID
	if orderID <= 0 {
		payment, err := q.GetPayment(ctx, args.PaymentID)
		if err != nil {
			return err
		}
		orderID = payment.OrderID
	}
	if orderID > 0 {
		if err := q.CancelOrder(ctx, orderID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
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

func mapAlipayTradeState(status string) (biz.TradeState, string) {
	switch status {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		return biz.TradeStateSuccess, status
	case "WAIT_BUYER_PAY":
		return biz.TradeStateNotPay, status
	case "TRADE_CLOSED":
		return biz.TradeStateClosed, status
	default:
		return biz.TradeStateUnspecified, status
	}
}
