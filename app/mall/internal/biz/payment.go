package biz

import (
	"context"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
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
	NotifyURL   string
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

const CheckWechatPayJobKind = "check_wechat_pay"

type CheckWechatPayArgs struct {
	PaymentID           int64  `json:"payment_id"`
	OrderID             int64  `json:"order_id"`
	OutTradeNo          string `json:"out_trade_no" river:"unique"`
	MaxPolls            int    `json:"max_polls"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	Source              string `json:"source"`
}

func (CheckWechatPayArgs) Kind() string {
	return CheckWechatPayJobKind
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

type PaymentMQRepo interface {
	EnqueueCheckWechatPay(ctx context.Context, args CheckWechatPayArgs, scheduledAt time.Time) (*MQJob, error)
	GetMQJob(ctx context.Context, jobID int64) (*MQJob, error)
}

type PaymentSyncRepo interface {
	ApplyWechatPayQuery(ctx context.Context, args CheckWechatPayArgs, result *PaymentQueryResult) error
	MarkWechatPayExpired(ctx context.Context, args CheckWechatPayArgs) error
}

type PaymentJobUsecase struct {
	repo PaymentMQRepo
	log  *log.Helper
}

func NewPaymentJobUsecase(repo PaymentMQRepo, logger log.Logger) *PaymentJobUsecase {
	return &PaymentJobUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *PaymentJobUsecase) EnqueueCheckWechatPay(ctx context.Context, args CheckWechatPayArgs, delay time.Duration) (*MQJob, error) {
	args = normalizeCheckWechatPayArgs(args)
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
	job, err := uc.repo.EnqueueCheckWechatPay(ctx, args, scheduledAt)
	if err != nil {
		return nil, err
	}
	uc.log.WithContext(ctx).Infof("enqueued wechat pay check job_id=%d out_trade_no=%s", job.ID, args.OutTradeNo)
	return job, nil
}

func (uc *PaymentJobUsecase) GetMQJob(ctx context.Context, jobID int64) (*MQJob, error) {
	if jobID <= 0 {
		return nil, errors.BadRequest("MQ_JOB_ID_REQUIRED", "job_id is required")
	}
	return uc.repo.GetMQJob(ctx, jobID)
}

func normalizeCheckWechatPayArgs(args CheckWechatPayArgs) CheckWechatPayArgs {
	if args.MaxPolls <= 0 {
		args.MaxPolls = 5
	}
	if args.PollIntervalSeconds <= 0 {
		args.PollIntervalSeconds = 30
	}
	if args.Source == "" {
		args.Source = "api"
	}
	return args
}
