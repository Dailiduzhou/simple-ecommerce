package biz

import (
	"context"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
)

type PayChannel string

const (
	Wechat PayChannel = "wechat"
	Alipay PayChannel = "alipay"
)

type TradeState string

const (
	TradeStateUnspecified TradeState = "TRADE_STATE_UNSPECIFIED"
	TradeStateSuccess     TradeState = "SUCCESS"
	TradeStateRefund      TradeState = "REFUND"
	TradeStateNotPay      TradeState = "NOTPAY"
	TradeStateClosed      TradeState = "CLOSED"
	TradeStateRevoked     TradeState = "REVOKED"
	TradeStateUserPaying  TradeState = "USERPAYING"
	TradeStatePayError    TradeState = "PAYERROR"
)

func (s TradeState) String() string {
	return string(s)
}

func (s TradeState) IsTerminal() bool {
	switch s {
	case TradeStateSuccess, TradeStateRefund, TradeStateClosed, TradeStateRevoked, TradeStatePayError:
		return true
	default:
		return false
	}
}

func (s TradeState) IsPending() bool {
	switch s {
	case TradeStateNotPay, TradeStateUserPaying, TradeStateUnspecified:
		return true
	default:
		return false
	}
}

func NormalizePayChannel(channel string) string {
	return strings.ToLower(strings.TrimSpace(channel))
}

func ParseTradeState(state string) TradeState {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case TradeStateSuccess.String():
		return TradeStateSuccess
	case TradeStateRefund.String():
		return TradeStateRefund
	case TradeStateNotPay.String():
		return TradeStateNotPay
	case TradeStateClosed.String():
		return TradeStateClosed
	case TradeStateRevoked.String():
		return TradeStateRevoked
	case TradeStateUserPaying.String():
		return TradeStateUserPaying
	case TradeStatePayError.String():
		return TradeStatePayError
	default:
		return TradeStateUnspecified
	}
}

type PaymentPrepayRequest struct {
	Channel     string
	OutTradeNo  string
	Description string
	TotalAmount int32
	OpenID      string
}

type PaymentPrepayResult struct {
	Channel    string
	OutTradeNo string

	PrepayID  string
	AppID     string
	TimeStamp string
	NonceStr  string
	Package   string
	SignType  string
	PaySign   string

	CodeURL string
	PayURL  string
}

type PaymentQueryRequest struct {
	Channel       string
	OutTradeNo    string
	TransactionID string
}

type PaymentQueryResult struct {
	Channel        string
	OutTradeNo     string
	TransactionID  string
	TradeState     TradeState
	TradeStateDesc string
	RawTradeState  string
	TotalAmount    int32
}

type PaymentCloseRequest struct {
	Channel       string
	OutTradeNo    string
	TransactionID string
}

type PaymentCloseResult struct {
	Channel       string
	OutTradeNo    string
	TransactionID string
	Success       bool

	// RawCode / RawSubCode 透传支付宝业务响应码,供上层做精细化分类
	// (审计/告警/对账)。为空表示该次请求未到达业务层(transport/HTTP 错误)。
	RawCode    string
	RawSubCode string
}

type PaymentAdapter interface {
	Channel() string
	Prepay(ctx context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error)
	QueryOrder(ctx context.Context, req PaymentQueryRequest) (*PaymentQueryResult, error)
	CloseOrder(ctx context.Context, req PaymentCloseRequest) (*PaymentCloseResult, error)
}

type PaymentGateway interface {
	Prepay(ctx context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error)
	QueryOrder(ctx context.Context, req PaymentQueryRequest) (*PaymentQueryResult, error)
	CloseOrder(ctx context.Context, req PaymentCloseRequest) (*PaymentCloseResult, error)
}

const CheckPayJobKind = "check_pay"

type CheckPayArgs struct {
	PaymentID           int64  `json:"payment_id"`
	OrderID             int64  `json:"order_id"`
	OutTradeNo          string `json:"out_trade_no" river:"unique"`
	MaxPolls            int    `json:"max_polls"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	Source              string `json:"source"`
	Channel             string `json:"channel"`
}

func (CheckPayArgs) Kind() string {
	return CheckPayJobKind
}

type MQJob struct {
	ID          int64
	Kind        string
	Queue       string
	State       string
	Attempt     int
	MaxAttempts int
	ArgsJSON    string
	Tags        []string
	CreatedAt   time.Time
	ScheduledAt time.Time
	AttemptedAt *time.Time
	FinalizedAt *time.Time
	Errors      []MQJobError
}

type MQJobError struct {
	Attempt int
	Error   string
	At      time.Time
}

type PaymentDO struct {
	ID             int64
	OrderID        int64
	UserID         int64
	MerchantID     int64
	Amount         int32
	Status         string
	PayChannel     string
	OutTradeNo     string
	ThirdPartyTxID string
	PaidAt         *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type PaymentRepo interface {
	CreatePayment(ctx context.Context, args CreatePaymentArgs) (*PaymentDO, error)
	GetPayment(ctx context.Context, id int64) (*PaymentDO, error)
	GetPaymentByOrder(ctx context.Context, orderID int64) (*PaymentDO, error)
	GetActivePaymentByOrderChannel(ctx context.Context, orderID int64, channel string) (*PaymentDO, error)
	GetPaymentByOutTradeNo(ctx context.Context, outTradeNo string) (*PaymentDO, error)
}

type CreatePaymentArgs struct {
	OrderID    int64
	UserID     int64
	MerchantID int64
	Amount     int32
	PayChannel string
	OutTradeNo string // optional; server fills with a snowflake id if empty
}

type PaymentMQRepo interface {
	EnqueueCheckPay(ctx context.Context, args CheckPayArgs, scheduledAt time.Time) (*MQJob, error)
	EnqueueCheckPayTx(ctx context.Context, args CheckPayArgs, scheduledAt time.Time) (*MQJob, error)
	GetMQJob(ctx context.Context, jobID int64) (*MQJob, error)
}

type PaymentSyncRepo interface {
	ApplyPayQuery(ctx context.Context, args CheckPayArgs, result *PaymentQueryResult) error
	MarkPayExpired(ctx context.Context, args CheckPayArgs) error
}

type PaymentJobUsecase interface {
	EnqueueCheckPay(ctx context.Context, args CheckPayArgs, delay time.Duration) (*MQJob, error)
	EnqueueCheckPayTx(ctx context.Context, args CheckPayArgs, delay time.Duration) (*MQJob, error)
	GetMQJob(ctx context.Context, jobID int64) (*MQJob, error)
}

type paymentJobUsecase struct {
	repo PaymentMQRepo
	log  *log.Helper
}

func NewPaymentJobUsecase(repo PaymentMQRepo, logger log.Logger) PaymentJobUsecase {
	return &paymentJobUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *paymentJobUsecase) EnqueueCheckPay(ctx context.Context, args CheckPayArgs, delay time.Duration) (*MQJob, error) {
	args = NormalizeCheckPayArgs(args)
	if args.PaymentID <= 0 {
		return nil, errors.BadRequest("PAYMENT_ID_REQUIRED", "payment_id is required")
	}
	if args.OutTradeNo == "" {
		return nil, errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	var scheduledAt time.Time
	if delay > 0 {
		scheduledAt = time.Now().Add(delay)
	}
	job, err := uc.repo.EnqueueCheckPay(ctx, args, scheduledAt)
	if err != nil {
		return nil, err
	}
	uc.log.WithContext(ctx).Infof("enqueued pay check job_id=%d out_trade_no=%s channel=%s", job.ID, args.OutTradeNo, args.Channel)
	return job, nil
}

func (uc *paymentJobUsecase) EnqueueCheckPayTx(ctx context.Context, args CheckPayArgs, delay time.Duration) (*MQJob, error) {
	args = NormalizeCheckPayArgs(args)
	if args.PaymentID <= 0 {
		return nil, errors.BadRequest("PAYMENT_ID_REQUIRED", "payment_id is required")
	}
	if args.OutTradeNo == "" {
		return nil, errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	var scheduledAt time.Time
	if delay > 0 {
		scheduledAt = time.Now().Add(delay)
	}
	job, err := uc.repo.EnqueueCheckPayTx(ctx, args, scheduledAt)
	if err != nil {
		return nil, err
	}
	uc.log.WithContext(ctx).Infof("enqueued pay check tx job_id=%d out_trade_no=%s channel=%s", job.ID, args.OutTradeNo, args.Channel)
	return job, nil
}

func (uc *paymentJobUsecase) GetMQJob(ctx context.Context, jobID int64) (*MQJob, error) {
	if jobID <= 0 {
		return nil, errors.BadRequest("MQ_JOB_ID_REQUIRED", "job_id is required")
	}
	return uc.repo.GetMQJob(ctx, jobID)
}

// NormalizeCheckPayArgs applies defaults for a check-pay job. It is also used
// by the worker so that enqueue-time and run-time defaults stay consistent.
func NormalizeCheckPayArgs(args CheckPayArgs) CheckPayArgs {
	if args.MaxPolls <= 0 {
		args.MaxPolls = 5
	}
	if args.PollIntervalSeconds <= 0 {
		args.PollIntervalSeconds = 30
	}
	if args.Source == "" {
		args.Source = "api"
	}
	if args.Channel == "" {
		args.Channel = string(Wechat)
	}
	return args
}

type PaymentUsecase interface {
	CreatePayment(ctx context.Context, orderID, userID, merchantID int64, payChannel string) (*PaymentDO, error)
	GetPayment(ctx context.Context, id int64) (*PaymentDO, error)
	GetPaymentByOrder(ctx context.Context, orderID int64) (*PaymentDO, error)
	Prepay(ctx context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error)
	// PrepayForOrder 是统一支付 API 的入口:
	// 1) 通过 OrderNo 反查订单;
	// 2) 创建/复用支付流水;
	// 3) 调用三方 prepay;
	// 4) 返回 Payment + Prepay,service 层负责编码 action_type + payload。
	PrepayForOrder(ctx context.Context, args PrepayForOrderArgs) (*PrepayForOrderResult, error)
	// PrepayForOrderWithCheckJob 与 PrepayForOrder 相同,但在同一事务中额外
	// 入队一个微信支付轮询任务,保证支付流水写入与 MQ 入队原子性。
	// checkJob 中 PaymentID/OrderID/OutTradeNo 会被忽略,由 biz 层根据 prepay 结果填充。
	PrepayForOrderWithCheckJob(ctx context.Context, args PrepayForOrderArgs, checkJob CheckPayArgs, delay time.Duration) (*PrepayForOrderResult, *MQJob, error)
	// EnqueueWechatCheckJobByOutTradeNo 按商户订单号查询 payment 并在一个事务中
	// 入队微信支付轮询任务,用于微信异步通知后主动查询支付结果。
	// checkJob 中 PaymentID/OrderID/OutTradeNo 会被忽略,由 biz 层根据查询结果填充。
	EnqueueWechatCheckJobByOutTradeNo(ctx context.Context, outTradeNo string, checkJob CheckPayArgs) (*MQJob, error)
	QueryOrder(ctx context.Context, req PaymentQueryRequest) (*PaymentQueryResult, error)
	CloseOrder(ctx context.Context, req PaymentCloseRequest) (*PaymentCloseResult, error)
}

// PrepayForOrderArgs 统一支付入口入参。
// Channel 必须是 NormalizePayChannel 后的值(wechat / alipay),
// 渠道细分子类型(JSAPI / NATIVE / WAP / APP)由 service 层在编码 payload 时
// 区分,不在 biz 层体现。
type PrepayForOrderArgs struct {
	OrderNo     string
	Channel     string
	ClientIP    string
	ExtraParams map[string]string
	Description string
	TotalAmount int32
}

// PrepayForOrderResult 统一支付入口出参。
// Payment 是创建/复用的支付流水,Prepay 是三方 prepay 返回的原始结果。
// service 层根据 channel 编码成前端可用的 action_type + payload JSON。
type PrepayForOrderResult struct {
	Payment *PaymentDO
	Prepay  *PaymentPrepayResult
}

type paymentUsecase struct {
	gateway     PaymentGateway
	paymentRepo PaymentRepo
	orderRepo   OrderRepo
	paymentJobs PaymentJobUsecase
	tx          TxManager
	idGen       IDGenerator
	log         *log.Helper
}

func NewPaymentUsecase(gateway PaymentGateway, paymentRepo PaymentRepo, orderRepo OrderRepo, paymentJobs PaymentJobUsecase, tx TxManager, idGen IDGenerator, logger log.Logger) PaymentUsecase {
	return &paymentUsecase{
		gateway:     gateway,
		paymentRepo: paymentRepo,
		orderRepo:   orderRepo,
		paymentJobs: paymentJobs,
		tx:          tx,
		idGen:       idGen,
		log:         log.NewHelper(logger),
	}
}

func (uc *paymentUsecase) CreatePayment(ctx context.Context, orderID, userID, merchantID int64, payChannel string) (*PaymentDO, error) {
	order, err := uc.orderRepo.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	channel := NormalizePayChannel(payChannel)
	if channel == "" {
		channel = string(Alipay)
	}

	// Idempotency: short-circuit on an existing active payment.
	if existing, err := uc.paymentRepo.GetActivePaymentByOrderChannel(ctx, orderID, channel); err == nil && existing != nil {
		uc.log.WithContext(ctx).Infof("reusing existing active payment_id=%d order_id=%d channel=%s out_trade_no=%s", existing.ID, orderID, channel, existing.OutTradeNo)
		return existing, nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	outTradeNo, err := uc.resolveOutTradeNo(ctx, "")
	if err != nil {
		return nil, err
	}

	return uc.paymentRepo.CreatePayment(ctx, CreatePaymentArgs{
		OrderID:    orderID,
		UserID:     userID,
		MerchantID: merchantID,
		Amount:     order.TotalAmount,
		PayChannel: channel,
		OutTradeNo: outTradeNo,
	})
}

func (uc *paymentUsecase) resolveOutTradeNo(_ context.Context, supplied string) (string, error) {
	if supplied != "" {
		if err := validateOutTradeNo(supplied); err != nil {
			return "", err
		}
		return supplied, nil
	}
	return uc.idGen.GenerateString(), nil
}

// validateOutTradeNo enforces: required, ≤ 64 chars, charset [A-Za-z0-9_-].
// Wechat allows 32 chars, Alipay 64; 64 is the upper bound across both.
// 字符集限制防止注入特殊字符到第三方支付系统的 URL/表单字段中。
func validateOutTradeNo(s string) error {
	if s == "" {
		return errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	if len(s) > 64 {
		return errors.BadRequest("OUT_TRADE_NO_TOO_LONG", "out_trade_no must be ≤ 64 chars")
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return errors.BadRequest("OUT_TRADE_NO_INVALID_CHARSET", "out_trade_no must match [A-Za-z0-9_-]")
		}
	}
	return nil
}

// ErrPaymentConflict is returned when a CreatePayment insert collides on
// idx_payments_active_out_trade_no_channel. Translated to ALREADY_EXISTS / 409.
var ErrPaymentConflict = errors.Conflict("PAYMENT_OUT_TRADE_NO_CONFLICT",
	"a payment with this out_trade_no already exists for this channel")

func (uc *paymentUsecase) GetPayment(ctx context.Context, id int64) (*PaymentDO, error) {
	return uc.paymentRepo.GetPayment(ctx, id)
}

// PrepayForOrder 实现统一支付入口:order_no -> payment -> prepay。
// 复用 CreatePayment 的幂等逻辑(同 active payment+channel 复用),
// 内部再调 gateway.Prepay。service 层在拿到结果后编码 action_type / payload。
func (uc *paymentUsecase) PrepayForOrder(ctx context.Context, args PrepayForOrderArgs) (*PrepayForOrderResult, error) {
	// 1) 通过商户订单号反查订单。
	order, err := uc.orderRepo.GetOrderByOrderNo(ctx, args.OrderNo)
	if err != nil {
		return nil, err
	}

	// 2) 创建或复用支付流水。CreatePayment 内部会再调一次 GetOrder
	// (用 int64 PK),这里多一次往返是可接受的——复用幂等 + 复用业务校验。
	// 商户号不在订单上(由调用方提供),统一支付入口没有该上下文,
	// 传 0;payments.merchant_id 列允许为 0。
	payment, err := uc.CreatePayment(ctx, order.ID, order.UserID, 0, args.Channel)
	if err != nil {
		return nil, err
	}

	// 3) 调三方 prepay。从 extra_params 抽取渠道特有字段(openid 等)。
	openID := ""
	if args.ExtraParams != nil {
		openID = args.ExtraParams["openid"]
	}
	prepay, err := uc.Prepay(ctx, PaymentPrepayRequest{
		Channel:     args.Channel,
		OutTradeNo:  payment.OutTradeNo,
		Description: args.Description,
		TotalAmount: args.TotalAmount,
		OpenID:      openID,
	})
	if err != nil {
		return nil, err
	}

	return &PrepayForOrderResult{Payment: payment, Prepay: prepay}, nil
}

func (uc *paymentUsecase) PrepayForOrderWithCheckJob(ctx context.Context, args PrepayForOrderArgs, checkJob CheckPayArgs, delay time.Duration) (*PrepayForOrderResult, *MQJob, error) {
	var result *PrepayForOrderResult
	var job *MQJob
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		r, err := uc.PrepayForOrder(ctx, args)
		if err != nil {
			return err
		}
		result = r
		if args.Channel != string(Wechat) || uc.paymentJobs == nil {
			return nil
		}
		checkJob.PaymentID = r.Payment.ID
		checkJob.OrderID = r.Payment.OrderID
		checkJob.OutTradeNo = r.Payment.OutTradeNo
		checkJob.Channel = string(Wechat)
		j, err := uc.paymentJobs.EnqueueCheckPayTx(ctx, checkJob, delay)
		if err != nil {
			return err
		}
		job = j
		return nil
	})
	return result, job, err
}

func (uc *paymentUsecase) EnqueueWechatCheckJobByOutTradeNo(ctx context.Context, outTradeNo string, checkJob CheckPayArgs) (*MQJob, error) {
	if outTradeNo == "" {
		return nil, errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	if uc.paymentJobs == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_MQ_NOT_CONFIGURED", "payment mq is not configured")
	}
	var job *MQJob
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		payment, err := uc.paymentRepo.GetPaymentByOutTradeNo(ctx, outTradeNo)
		if err != nil {
			return err
		}
		checkJob.PaymentID = payment.ID
		checkJob.OrderID = payment.OrderID
		checkJob.OutTradeNo = payment.OutTradeNo
		checkJob.Channel = string(Wechat)
		j, err := uc.paymentJobs.EnqueueCheckPayTx(ctx, checkJob, 0)
		if err != nil {
			return err
		}
		job = j
		return nil
	})
	return job, err
}

func (uc *paymentUsecase) GetPaymentByOrder(ctx context.Context, orderID int64) (*PaymentDO, error) {
	return uc.paymentRepo.GetPaymentByOrder(ctx, orderID)
}

func (uc *paymentUsecase) Prepay(ctx context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error) {
	if uc.gateway == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_GATEWAY_NOT_CONFIGURED", "payment gateway is not configured")
	}
	req.Channel = NormalizePayChannel(req.Channel)
	if req.Channel == "" {
		req.Channel = string(Wechat)
	}
	return uc.gateway.Prepay(ctx, req)
}

func (uc *paymentUsecase) QueryOrder(ctx context.Context, req PaymentQueryRequest) (*PaymentQueryResult, error) {
	if uc.gateway == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_GATEWAY_NOT_CONFIGURED", "payment gateway is not configured")
	}
	req.Channel = NormalizePayChannel(req.Channel)
	if req.Channel == "" {
		req.Channel = string(Wechat)
	}
	return uc.gateway.QueryOrder(ctx, req)
}

func (uc *paymentUsecase) CloseOrder(ctx context.Context, req PaymentCloseRequest) (*PaymentCloseResult, error) {
	if uc.gateway == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_GATEWAY_NOT_CONFIGURED", "payment gateway is not configured")
	}
	req.Channel = NormalizePayChannel(req.Channel)
	if req.Channel == "" {
		req.Channel = string(Wechat)
	}
	return uc.gateway.CloseOrder(ctx, req)
}
