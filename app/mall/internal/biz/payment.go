package biz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
)

const (
	PaymentStatusCreating          = "creating"
	PaymentStatusPending           = "pending"
	PaymentStatusFailed            = "failed"
	PaymentStatusSuccess           = "success"
	PaymentStatusRefunded          = "refunded"
	PaymentStatusClosePending      = "close_pending"
	PaymentStatusClosed            = "closed"
	ReconciliationStatusNone       = "none"
	ReconciliationStatusRequired   = "required"
	ReconciliationStatusProcessing = "processing"
	ReconciliationStatusResolved   = "resolved"
)

var (
	ErrPaymentConflict               = errors.Conflict("PAYMENT_CONFLICT", "an active payment already exists")
	ErrPaymentNotFound               = errors.NotFound("PAYMENT_NOT_FOUND", "payment not found")
	ErrPaymentStateConflict          = errors.Conflict("PAYMENT_STATE_CONFLICT", "payment state transition conflicts with the current state")
	ErrPaymentNotificationBinding    = errors.Conflict("PAYMENT_NOTIFICATION_BINDING_MISMATCH", "payment notification does not match reconciliation job")
	ErrPaymentProviderUnavailable    = errors.ServiceUnavailable("PAYMENT_PROVIDER_NOT_AVAILABLE", "payment provider is not available")
	ErrPaymentPrepayInProgress       = errors.Conflict("PAYMENT_PREPAY_IN_PROGRESS", "payment prepay is already in progress")
	ErrPaymentReconciliationRequired = errors.Conflict("PAYMENT_RECONCILIATION_REQUIRED", "payment requires reconciliation")
	ErrOrderExpired                  = errors.Conflict("ORDER_EXPIRED", "order payment window has expired")
	// ErrProviderOrderNotExist means the provider has no record of the trade:
	// the prepay never reached the provider or the order was purged. Safe to
	// treat as closed during the close flow.
	ErrProviderOrderNotExist = errors.NotFound("PAYMENT_PROVIDER_ORDER_NOT_EXIST", "provider has no record of this trade")
)

type PaymentMethod struct {
	Provider string
	Product  string
}

func (m PaymentMethod) Normalize() PaymentMethod {
	return PaymentMethod{Provider: strings.ToLower(strings.TrimSpace(m.Provider)), Product: strings.ToLower(strings.TrimSpace(m.Product))}
}

func (m PaymentMethod) String() string {
	m = m.Normalize()
	if m.Provider == "" || m.Product == "" {
		return ""
	}
	return m.Provider + ":" + m.Product
}

func ParsePaymentMethod(value string) (PaymentMethod, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return PaymentMethod{}, errors.BadRequest("PAYMENT_METHOD_INVALID", "payment method must be provider:product")
	}
	return PaymentMethod{Provider: parts[0], Product: parts[1]}, nil
}

type PaymentCapabilities struct {
	SupportsNotify bool
	RequiresPoll   bool
	SupportsClose  bool
	SupportsRefund bool
}

type PaymentActionType string

const (
	PaymentActionRedirect PaymentActionType = "redirect"
	PaymentActionForm     PaymentActionType = "form"
	PaymentActionInvoke   PaymentActionType = "invoke"
)

type PaymentAction struct {
	Type    PaymentActionType
	Payload json.RawMessage
}

type PaymentPrepayRequest struct {
	Method      PaymentMethod
	OutTradeNo  string
	Description string
	Amount      int64
	Currency    string
	ClientIP    string
	Extension   map[string]string
}

type PaymentPrepayResult struct {
	ProviderReference string
	Action            PaymentAction
}

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

func (s TradeState) String() string { return string(s) }
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

func ParseTradeState(state string) TradeState {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case string(TradeStateSuccess):
		return TradeStateSuccess
	case string(TradeStateRefund):
		return TradeStateRefund
	case string(TradeStateNotPay):
		return TradeStateNotPay
	case string(TradeStateClosed):
		return TradeStateClosed
	case string(TradeStateRevoked):
		return TradeStateRevoked
	case string(TradeStateUserPaying):
		return TradeStateUserPaying
	case string(TradeStatePayError):
		return TradeStatePayError
	default:
		return TradeStateUnspecified
	}
}

type PaymentQueryRequest struct {
	Method        PaymentMethod
	OutTradeNo    string
	TransactionID string
}

type PaymentQueryResult struct {
	Method         PaymentMethod
	OutTradeNo     string
	TransactionID  string
	TradeState     TradeState
	TradeStateDesc string
	RawTradeState  string
	Amount         int64
	Currency       string
}

type PaymentCloseRequest struct {
	Method        PaymentMethod
	OutTradeNo    string
	TransactionID string
}

type PaymentCloseResult struct {
	Method        PaymentMethod
	OutTradeNo    string
	TransactionID string
	Success       bool
	RawCode       string
	RawSubCode    string
}

type PaymentRefundRequest struct {
	Method        PaymentMethod
	OutTradeNo    string
	TransactionID string
	OutRefundNo   string
	Amount        int64
	Currency      string
	Reason        string
}

type PaymentRefundResult struct {
	Method        PaymentMethod
	OutTradeNo    string
	TransactionID string
	OutRefundNo   string
	Amount        int64
	Currency      string
	FundChanged   bool
	Success       bool
	RawCode       string
}

const (
	PaymentRefundStatusPending = "pending"
	PaymentRefundStatusSuccess = "success"
	PaymentRefundStatusFailed  = "failed"
)

type PaymentRefund struct {
	ID           int64
	PaymentID    int64
	OrderID      int64
	UserID       int64
	OutRefundNo  string
	TotalAmount  int64
	RefundAmount int64
	Currency     string
	Reason       string
	Status       string
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type PaymentNotification struct {
	Provider        string
	ProviderEventID string
	OutTradeNo      string
	TransactionID   string
	Amount          int64
	Currency        string
	PayloadHash     string
	VerifiedAt      time.Time
}

const (
	PaymentNotificationStatusReceived   = "received"
	PaymentNotificationStatusProcessing = "processing"
	PaymentNotificationStatusProcessed  = "processed"
	PaymentNotificationStatusFailed     = "failed"
)

type PaymentNotificationAck struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

func DefaultPaymentNotificationAck() PaymentNotificationAck {
	return PaymentNotificationAck{StatusCode: http.StatusBadRequest, ContentType: "text/plain; charset=utf-8", Body: []byte("unsupported provider")}
}

type PaymentNotificationAcknowledger interface {
	NotificationAck(provider string, success bool) PaymentNotificationAck
}

type PaymentNotificationProviderChecker interface {
	SupportsNotificationProvider(provider string) bool
}

type PaymentAdapter interface {
	Provider() string
	Supports(method PaymentMethod) bool
	Capabilities(method PaymentMethod) PaymentCapabilities
	Prepay(context.Context, PaymentPrepayRequest) (*PaymentPrepayResult, error)
	Query(context.Context, PaymentQueryRequest) (*PaymentQueryResult, error)
	Close(context.Context, PaymentCloseRequest) (*PaymentCloseResult, error)
	Refund(context.Context, PaymentRefundRequest) (*PaymentRefundResult, error)
	ParseAndVerifyNotification(*http.Request) (*PaymentNotification, error)
	NotificationAck(success bool) PaymentNotificationAck
}

type PaymentGateway interface {
	Capabilities(PaymentMethod) (PaymentCapabilities, error)
	Prepay(context.Context, PaymentPrepayRequest) (*PaymentPrepayResult, error)
	Query(context.Context, PaymentQueryRequest) (*PaymentQueryResult, error)
	Close(context.Context, PaymentCloseRequest) (*PaymentCloseResult, error)
	Refund(context.Context, PaymentRefundRequest) (*PaymentRefundResult, error)
	ParseAndVerifyNotification(string, *http.Request) (*PaymentNotification, error)
	NotificationAck(string, bool) (PaymentNotificationAck, error)
}

const CheckPayJobKind = "check_pay"
const ClosePayJobKind = "close_pay"

type CheckPayArgs struct {
	PaymentID           int64  `json:"payment_id" river:"unique"`
	Provider            string `json:"provider" river:"unique"`
	NotificationID      int64  `json:"notification_id" river:"unique"`
	Trigger             string `json:"trigger"`
	PollCount           int    `json:"poll_count"`
	MaxPolls            int    `json:"max_polls"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

func (CheckPayArgs) Kind() string { return CheckPayJobKind }

type ClosePayArgs struct {
	PaymentID int64  `json:"payment_id" river:"unique"`
	Provider  string `json:"provider" river:"unique"`
	Reason    string `json:"reason"`
}

func (ClosePayArgs) Kind() string { return ClosePayJobKind }

func NormalizeCheckPayArgs(args CheckPayArgs) CheckPayArgs {
	args.Provider = strings.ToLower(strings.TrimSpace(args.Provider))
	if args.MaxPolls <= 0 {
		args.MaxPolls = 5
	}
	if args.PollIntervalSeconds <= 0 {
		args.PollIntervalSeconds = 30
	}
	if args.Trigger == "" {
		args.Trigger = "api"
	}
	return args
}

type MQJob struct {
	ID                       int64
	Deduplicated             bool
	Kind, Queue, State       string
	Attempt, MaxAttempts     int
	ArgsJSON                 string
	Tags                     []string
	CreatedAt, ScheduledAt   time.Time
	AttemptedAt, FinalizedAt *time.Time
	Errors                   []MQJobError
}
type MQJobError struct {
	Attempt int
	Error   string
	At      time.Time
}

type PaymentDO struct {
	ID, OrderID, UserID, MerchantID                                  int64
	Amount                                                           int64
	Currency, Status, Method, OutTradeNo, ThirdPartyTxID             string
	ReconciliationStatus, ReconciliationReason, ReconciliationDetail string
	PrepayLeaseToken, LastError                                      string
	PrepayLeaseUntil                                                 *time.Time
	PrepayAttempts                                                   int32
	Action                                                           PaymentAction
	PaidAt                                                           *time.Time
	CreatedAt, UpdatedAt                                             time.Time
}

type CreatePaymentArgs struct {
	OrderID, UserID, MerchantID  int64
	Amount                       int64
	Currency, Method, OutTradeNo string
}

type ReconciliationFailure struct {
	PaymentID      int64
	NotificationID int64
	Provider       string
	RiverJobID     *int64
	Attempt        int
	Reason         string
	LastError      string
}

type PaymentRepo interface {
	CreatePayment(context.Context, CreatePaymentArgs) (*PaymentDO, error)
	GetPayment(context.Context, int64) (*PaymentDO, error)
	GetPaymentByUser(context.Context, int64, int64) (*PaymentDO, error)
	GetLatestPaymentByOrder(context.Context, int64) (*PaymentDO, error)
	GetActivePaymentByOrderMethod(context.Context, int64, string) (*PaymentDO, error)
	GetPaymentByOutTradeNo(context.Context, string) (*PaymentDO, error)
	BeginPaymentNotificationProcessing(context.Context, int64, string, string) (bool, error)
	RecordPaymentNotificationError(context.Context, int64, string) error
	MarkPaymentNotificationFailed(context.Context, int64, string) error
	ApplyPayQuery(context.Context, CheckPayArgs, *PaymentQueryResult) error
	MarkPayClosePending(context.Context, CheckPayArgs) error
	PreparePaymentRefund(context.Context, int64, string) (*PaymentDO, *PaymentRefund, error)
	RecordPaymentRefundError(context.Context, int64, string, bool) error
	ApplyPaymentRefund(context.Context, int64, int64) error
	MarkReconciliationRequired(context.Context, ReconciliationFailure) error
	RecordReconciliationFailure(context.Context, ReconciliationFailure) error
}

type PaymentPrepayRepo interface {
	ClaimPaymentPrepay(context.Context, int64, string, time.Duration) (*PaymentDO, error)
	FinalizePaymentPrepay(context.Context, int64, string, PaymentAction) (*PaymentDO, error)
	RecordPaymentPrepayError(context.Context, int64, string, string) error
}

type PaymentNotificationRepo interface {
	PersistAndEnqueueNotification(context.Context, *PaymentNotification, CheckPayArgs) (bool, error)
}

type PaymentMQRepo interface {
	EnqueueCheckPay(context.Context, CheckPayArgs, time.Time) (*MQJob, error)
	EnqueueCheckPayTx(context.Context, CheckPayArgs, time.Time) (*MQJob, error)
	EnqueueClosePay(context.Context, ClosePayArgs, time.Time) (*MQJob, error)
	EnqueueClosePayTx(context.Context, ClosePayArgs, time.Time) (*MQJob, error)
	GetMQJob(context.Context, int64) (*MQJob, error)
}

type PaymentJobUsecase interface {
	EnqueueCheckPay(context.Context, CheckPayArgs, time.Duration) (*MQJob, error)
	EnqueueCheckPayTx(context.Context, CheckPayArgs, time.Duration) (*MQJob, error)
	EnqueueClosePay(context.Context, ClosePayArgs, time.Duration) (*MQJob, error)
	EnqueueClosePayTx(context.Context, ClosePayArgs, time.Duration) (*MQJob, error)
	GetMQJob(context.Context, int64) (*MQJob, error)
}

type paymentJobUsecase struct {
	repo PaymentMQRepo
	log  *log.Helper
}

func NewPaymentJobUsecase(repo PaymentMQRepo, logger log.Logger) PaymentJobUsecase {
	return &paymentJobUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *paymentJobUsecase) enqueue(ctx context.Context, args CheckPayArgs, delay time.Duration, tx bool) (*MQJob, error) {
	args = NormalizeCheckPayArgs(args)
	if args.PaymentID <= 0 {
		return nil, errors.BadRequest("PAYMENT_ID_REQUIRED", "payment_id is required")
	}
	if args.Provider == "" {
		return nil, errors.BadRequest("PAYMENT_PROVIDER_REQUIRED", "payment provider is required")
	}
	var scheduledAt time.Time
	if delay > 0 {
		scheduledAt = time.Now().Add(delay)
	}
	var job *MQJob
	var err error
	if tx {
		job, err = uc.repo.EnqueueCheckPayTx(ctx, args, scheduledAt)
	} else {
		job, err = uc.repo.EnqueueCheckPay(ctx, args, scheduledAt)
	}
	if err == nil {
		uc.log.WithContext(ctx).Infow("msg", "enqueued payment reconciliation", "job_id", job.ID, "payment_id", args.PaymentID, "provider", args.Provider, "trigger", args.Trigger)
	}
	return job, err
}
func (uc *paymentJobUsecase) EnqueueCheckPay(ctx context.Context, args CheckPayArgs, delay time.Duration) (*MQJob, error) {
	return uc.enqueue(ctx, args, delay, false)
}
func (uc *paymentJobUsecase) EnqueueCheckPayTx(ctx context.Context, args CheckPayArgs, delay time.Duration) (*MQJob, error) {
	return uc.enqueue(ctx, args, delay, true)
}
func (uc *paymentJobUsecase) GetMQJob(ctx context.Context, id int64) (*MQJob, error) {
	if id <= 0 {
		return nil, errors.BadRequest("MQ_JOB_ID_REQUIRED", "job_id is required")
	}
	return uc.repo.GetMQJob(ctx, id)
}

func (uc *paymentJobUsecase) enqueueClose(ctx context.Context, args ClosePayArgs, delay time.Duration, tx bool) (*MQJob, error) {
	args.Provider = strings.ToLower(strings.TrimSpace(args.Provider))
	if args.PaymentID <= 0 || args.Provider == "" {
		return nil, errors.BadRequest("PAYMENT_CLOSE_JOB_INVALID", "payment_id and provider are required")
	}
	var scheduledAt time.Time
	if delay > 0 {
		scheduledAt = time.Now().Add(delay)
	}
	if tx {
		return uc.repo.EnqueueClosePayTx(ctx, args, scheduledAt)
	}
	return uc.repo.EnqueueClosePay(ctx, args, scheduledAt)
}

func (uc *paymentJobUsecase) EnqueueClosePay(ctx context.Context, args ClosePayArgs, delay time.Duration) (*MQJob, error) {
	return uc.enqueueClose(ctx, args, delay, false)
}

func (uc *paymentJobUsecase) EnqueueClosePayTx(ctx context.Context, args ClosePayArgs, delay time.Duration) (*MQJob, error) {
	return uc.enqueueClose(ctx, args, delay, true)
}

type OrderExpiryRepo interface {
	ExpireOrder(context.Context, int64) error
}

type PrepayForOrderArgs struct {
	OrderNo     string
	UserID      int64
	Method      PaymentMethod
	ClientIP    string
	Extension   map[string]string
	Description string
}
type PrepayForOrderResult struct {
	Payment *PaymentDO
	Prepay  *PaymentPrepayResult
}

type PaymentUsecase interface {
	PrepayForOrder(context.Context, PrepayForOrderArgs) (*PrepayForOrderResult, error)
	GetPayment(context.Context, int64, int64) (*PaymentDO, error)
	GetPaymentByOrder(context.Context, int64, int64) (*PaymentDO, error)
	QueryPayment(context.Context, string, int64) (*PaymentQueryResult, error)
	ClosePayment(context.Context, string, int64) (*PaymentCloseResult, error)
	RefundPayment(context.Context, int64) (*PaymentRefundResult, error)
	CreateCheckJob(context.Context, int64, int, time.Duration, time.Duration, string) (*MQJob, error)
	HandleNotification(context.Context, string, *http.Request) error
}

type paymentUsecase struct {
	gateway          PaymentGateway
	paymentRepo      PaymentRepo
	notificationRepo PaymentNotificationRepo
	orderRepo        OrderRepo
	paymentJobs      PaymentJobUsecase
	tx               TxManager
	idGen            IDGenerator
	policy           PaymentPolicy
	log              *log.Helper
}

type PaymentPolicy struct {
	PrepayLeaseDuration time.Duration
	PollInitialDelay    time.Duration
	PollInterval        time.Duration
	PollMaxCount        int
}

func NewPaymentUsecase(gateway PaymentGateway, paymentRepo PaymentRepo, notificationRepo PaymentNotificationRepo, orderRepo OrderRepo, paymentJobs PaymentJobUsecase, tx TxManager, idGen IDGenerator, logger log.Logger) PaymentUsecase {
	return NewConfiguredPaymentUsecase(gateway, paymentRepo, notificationRepo, orderRepo, paymentJobs, tx, idGen, PaymentPolicy{}, logger)
}

func NewConfiguredPaymentUsecase(gateway PaymentGateway, paymentRepo PaymentRepo, notificationRepo PaymentNotificationRepo, orderRepo OrderRepo, paymentJobs PaymentJobUsecase, tx TxManager, idGen IDGenerator, policy PaymentPolicy, logger log.Logger) PaymentUsecase {
	if policy.PrepayLeaseDuration <= 0 {
		policy.PrepayLeaseDuration = 30 * time.Second
	}
	if policy.PollInitialDelay <= 0 {
		policy.PollInitialDelay = 5 * time.Second
	}
	if policy.PollInterval <= 0 {
		policy.PollInterval = 10 * time.Second
	}
	if policy.PollMaxCount <= 0 {
		policy.PollMaxCount = 30
	}
	return &paymentUsecase{gateway: gateway, paymentRepo: paymentRepo, notificationRepo: notificationRepo, orderRepo: orderRepo, paymentJobs: paymentJobs, tx: tx, idGen: idGen, policy: policy, log: log.NewHelper(logger)}
}

func (uc *paymentUsecase) PrepayForOrder(ctx context.Context, args PrepayForOrderArgs) (*PrepayForOrderResult, error) {
	prepayRepo, ok := uc.paymentRepo.(PaymentPrepayRepo)
	if !ok {
		return nil, errors.ServiceUnavailable("PAYMENT_PREPAY_REPO_UNAVAILABLE", "payment prepay repository is unavailable")
	}
	method := args.Method.Normalize()
	if method.String() == "" {
		return nil, errors.BadRequest("PAYMENT_METHOD_REQUIRED", "payment method is required")
	}
	order, err := uc.orderRepo.GetOrderByOrderNo(ctx, args.OrderNo)
	if err != nil {
		return nil, err
	}
	if args.UserID <= 0 || order.UserID != args.UserID {
		return nil, ErrOrderNotFound
	}
	if order.Status != OrderStatusPendingPayment {
		return nil, errors.Conflict("ORDER_NOT_PAYABLE", "order is not awaiting payment")
	}

	methodKey := method.String()
	if !order.ExpiresAt.IsZero() && !time.Now().UTC().Before(order.ExpiresAt) {
		return nil, ErrOrderExpired
	}
	outTradeNo := uc.idGen.GenerateString()
	if err := validateOutTradeNo(outTradeNo); err != nil {
		return nil, err
	}
	payment, err := uc.paymentRepo.CreatePayment(ctx, CreatePaymentArgs{
		OrderID: order.ID, UserID: order.UserID, Amount: order.TotalAmount,
		Currency: order.Currency, Method: methodKey, OutTradeNo: outTradeNo,
	})
	if err != nil {
		return nil, err
	}
	if payment.Status == PaymentStatusPending && payment.Action.Type != "" {
		return &PrepayForOrderResult{Payment: payment, Prepay: &PaymentPrepayResult{ProviderReference: payment.ThirdPartyTxID, Action: payment.Action}}, nil
	}
	if payment.Status != PaymentStatusCreating {
		if payment.Status == PaymentStatusPending {
			// pending 却没有 action 属于适配器违约:支付已激活但无法把支付参数交给用户。
			uc.log.WithContext(ctx).Errorw("msg", "pending payment has no action", "payment_id", payment.ID, "method", payment.Method)
		}
		return nil, ErrPaymentStateConflict
	}
	// Capabilities 是纯本地查询,放在渠道建单之前:一旦它失败,不会留下
	// "渠道已建单但本地无法收尾" 的中间态。
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
	}
	leaseToken := uc.idGen.GenerateString()
	payment, err = prepayRepo.ClaimPaymentPrepay(ctx, payment.ID, leaseToken, uc.policy.PrepayLeaseDuration)
	if err != nil {
		return nil, err
	}
	if payment.Status == PaymentStatusPending && payment.Action.Type != "" {
		return &PrepayForOrderResult{
			Payment: payment,
			Prepay:  &PaymentPrepayResult{ProviderReference: payment.ThirdPartyTxID, Action: payment.Action},
		}, nil
	}
	description := strings.TrimSpace(args.Description)
	if description == "" {
		description = fmt.Sprintf("Order %s", order.OutTradeNo)
	}
	prepay, err := uc.gateway.Prepay(ctx, PaymentPrepayRequest{
		Method: method, OutTradeNo: payment.OutTradeNo, Description: description,
		Amount: order.TotalAmount, Currency: order.Currency, ClientIP: args.ClientIP, Extension: args.Extension,
	})
	if err != nil {
		if recordErr := prepayRepo.RecordPaymentPrepayError(ctx, payment.ID, leaseToken, err.Error()); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record payment prepay error failed", "payment_id", payment.ID, "error", recordErr)
		}
		return nil, err
	}
	if prepay == nil || prepay.Action.Type == "" || len(prepay.Action.Payload) == 0 {
		err = errors.InternalServer("PAYMENT_PREPAY_RESPONSE_INVALID", "payment provider returned invalid prepay parameters")
		if recordErr := prepayRepo.RecordPaymentPrepayError(ctx, payment.ID, leaseToken, err.Error()); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record invalid payment prepay response failed", "payment_id", payment.ID, "error", recordErr)
		}
		return nil, err
	}

	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		activated, err := prepayRepo.FinalizePaymentPrepay(ctx, payment.ID, leaseToken, prepay.Action)
		if err != nil {
			return err
		}
		payment = activated
		if capabilities.RequiresPoll && uc.paymentJobs != nil {
			_, err = uc.paymentJobs.EnqueueCheckPayTx(ctx, CheckPayArgs{
				PaymentID: payment.ID, Provider: method.Provider, Trigger: "prepay",
				MaxPolls: uc.policy.PollMaxCount, PollIntervalSeconds: int(uc.policy.PollInterval.Seconds()),
			}, uc.policy.PollInitialDelay)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &PrepayForOrderResult{
		Payment: payment,
		Prepay:  &PaymentPrepayResult{ProviderReference: prepay.ProviderReference, Action: payment.Action},
	}, nil
}

func (uc *paymentUsecase) GetPayment(ctx context.Context, id, userID int64) (*PaymentDO, error) {
	return uc.paymentRepo.GetPaymentByUser(ctx, id, userID)
}

func (uc *paymentUsecase) GetPaymentByOrder(ctx context.Context, orderID, userID int64) (*PaymentDO, error) {
	order, err := uc.orderRepo.GetOrderByUser(ctx, orderID, userID)
	if err != nil {
		return nil, err
	}
	return uc.paymentRepo.GetLatestPaymentByOrder(ctx, order.ID)
}

func (uc *paymentUsecase) QueryPayment(ctx context.Context, outTradeNo string, userID int64) (*PaymentQueryResult, error) {
	payment, err := uc.authorizedByOutTradeNo(ctx, outTradeNo, userID)
	if err != nil {
		return nil, err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return nil, err
	}
	result, err := uc.gateway.Query(ctx, PaymentQueryRequest{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID})
	if err != nil {
		return nil, err
	}
	if result.TradeState.IsTerminal() {
		err = uc.paymentRepo.ApplyPayQuery(ctx, CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "api_query"}, result)
	}
	return result, err
}

func (uc *paymentUsecase) ClosePayment(ctx context.Context, outTradeNo string, userID int64) (*PaymentCloseResult, error) {
	payment, err := uc.authorizedByOutTradeNo(ctx, outTradeNo, userID)
	if err != nil {
		return nil, err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return nil, err
	}
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
	}
	if !capabilities.SupportsClose {
		return nil, errors.New(501, "PAYMENT_CLOSE_NOT_SUPPORTED", "provider does not support close")
	}
	if payment.Status == PaymentStatusClosed {
		return &PaymentCloseResult{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID, Success: true}, nil
	}
	if payment.Status != PaymentStatusPending && payment.Status != PaymentStatusClosePending {
		return nil, ErrPaymentStateConflict
	}
	if uc.paymentJobs == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_MQ_NOT_CONFIGURED", "payment mq is required for reliable close")
	}
	closeArgs := CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "api_close"}
	if err := uc.paymentRepo.MarkPayClosePending(ctx, closeArgs); err != nil {
		return nil, err
	}
	result, err := uc.gateway.Close(ctx, PaymentCloseRequest{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID})
	if err != nil {
		return nil, err
	}
	if result.Success {
		err = uc.paymentRepo.ApplyPayQuery(ctx, CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "api_close"}, &PaymentQueryResult{
			Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: result.TransactionID,
			TradeState: TradeStateClosed, Amount: payment.Amount, Currency: payment.Currency,
		})
	}
	return result, err
}

func (uc *paymentUsecase) RefundPayment(ctx context.Context, paymentID int64) (*PaymentRefundResult, error) {
	if paymentID <= 0 {
		return nil, errors.BadRequest("PAYMENT_ID_REQUIRED", "payment_id is required")
	}
	if uc.idGen == nil {
		return nil, errors.InternalServer("PAYMENT_ID_GENERATOR_MISSING", "refund number generator is unavailable")
	}
	payment, err := uc.paymentRepo.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return nil, err
	}
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
	}
	if !capabilities.SupportsRefund {
		return nil, errors.New(501, "PAYMENT_REFUND_NOT_SUPPORTED", "provider does not support refund")
	}
	payment, refund, err := uc.paymentRepo.PreparePaymentRefund(
		ctx,
		paymentID,
		uc.idGen.GenerateOrderNo64("rfnd", payment.UserID),
	)
	if err != nil {
		return nil, err
	}
	result := &PaymentRefundResult{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID,
		OutRefundNo: refund.OutRefundNo, Amount: refund.RefundAmount, Currency: refund.Currency,
	}
	if refund.Status == PaymentRefundStatusSuccess {
		result.Success = true
		return result, nil
	}
	result, err = uc.gateway.Refund(ctx, PaymentRefundRequest{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID,
		OutRefundNo: refund.OutRefundNo, Amount: refund.RefundAmount, Currency: refund.Currency,
		Reason: refund.Reason,
	})
	if err != nil {
		definitive := result != nil && result.RawCode != ""
		if recordErr := uc.paymentRepo.RecordPaymentRefundError(ctx, refund.ID, err.Error(), definitive); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record payment refund error failed", "payment_id", paymentID, "refund_id", refund.ID, "error", recordErr)
		}
		return result, err
	}
	if result == nil || !result.Success {
		err = errors.New(502, "PAYMENT_REFUND_FAILED", "payment provider did not confirm refund")
		if recordErr := uc.paymentRepo.RecordPaymentRefundError(ctx, refund.ID, err.Error(), true); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record payment refund rejection failed", "payment_id", paymentID, "refund_id", refund.ID, "error", recordErr)
		}
		return result, err
	}
	if err := uc.paymentRepo.ApplyPaymentRefund(ctx, paymentID, refund.ID); err != nil {
		return nil, err
	}
	return result, nil
}

func (uc *paymentUsecase) HandleNotification(ctx context.Context, provider string, request *http.Request) error {
	if uc.notificationRepo == nil {
		return errors.ServiceUnavailable("PAYMENT_CALLBACK_STORE_UNAVAILABLE", "payment callback store is unavailable")
	}
	notification, err := uc.gateway.ParseAndVerifyNotification(provider, request)
	if err != nil {
		return err
	}
	payment, err := uc.paymentRepo.GetPaymentByOutTradeNo(ctx, notification.OutTradeNo)
	if err != nil {
		return err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return err
	}
	if method.Provider != strings.ToLower(notification.Provider) {
		return errors.BadRequest("PAYMENT_NOTIFICATION_PROVIDER_MISMATCH", "notification provider does not match payment")
	}
	if notification.Amount != payment.Amount || strings.ToUpper(strings.TrimSpace(notification.Currency)) != strings.ToUpper(strings.TrimSpace(payment.Currency)) {
		reason := "callback_amount_mismatch"
		if notification.Amount == payment.Amount {
			reason = "callback_currency_mismatch"
		}
		if err := uc.paymentRepo.MarkReconciliationRequired(ctx, ReconciliationFailure{
			PaymentID: payment.ID, Provider: method.Provider, Attempt: 1,
			Reason: reason, LastError: "verified payment notification does not match persisted payment",
		}); err != nil {
			return err
		}
		return errors.Conflict("PAYMENT_NOTIFICATION_AMOUNT_MISMATCH", "payment notification amount or currency does not match payment")
	}
	_, err = uc.notificationRepo.PersistAndEnqueueNotification(ctx, notification, CheckPayArgs{
		PaymentID: payment.ID, Provider: method.Provider, Trigger: "callback",
		MaxPolls: uc.policy.PollMaxCount, PollIntervalSeconds: int(uc.policy.PollInterval.Seconds()),
	})
	return err
}

func (uc *paymentUsecase) NotificationAck(provider string, success bool) PaymentNotificationAck {
	ack, err := uc.gateway.NotificationAck(provider, success)
	if err != nil {
		return DefaultPaymentNotificationAck()
	}
	return ack
}

func (uc *paymentUsecase) SupportsNotificationProvider(provider string) bool {
	_, err := uc.gateway.NotificationAck(provider, false)
	return err == nil
}

func (uc *paymentUsecase) CreateCheckJob(ctx context.Context, paymentID int64, maxPolls int, pollInterval time.Duration, delay time.Duration, trigger string) (*MQJob, error) {
	if uc.paymentJobs == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_MQ_NOT_CONFIGURED", "payment mq is not configured")
	}
	payment, err := uc.paymentRepo.GetPayment(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	method, err := ParsePaymentMethod(payment.Method)
	if err != nil {
		return nil, err
	}
	return uc.paymentJobs.EnqueueCheckPay(ctx, CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: trigger, MaxPolls: maxPolls, PollIntervalSeconds: int(pollInterval.Seconds())}, delay)
}

func (uc *paymentUsecase) authorizedByOutTradeNo(ctx context.Context, outTradeNo string, userID int64) (*PaymentDO, error) {
	if outTradeNo == "" || userID <= 0 {
		return nil, ErrPaymentNotFound
	}
	payment, err := uc.paymentRepo.GetPaymentByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return nil, err
	}
	if payment == nil || payment.UserID != userID {
		return nil, ErrPaymentNotFound
	}
	return payment, nil
}

func validateOutTradeNo(value string) error {
	if value == "" {
		return errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	if len(value) > 64 {
		return errors.BadRequest("OUT_TRADE_NO_TOO_LONG", "out_trade_no must be at most 64 characters")
	}
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return errors.BadRequest("OUT_TRADE_NO_INVALID", "out_trade_no contains invalid characters")
	}
	return nil
}
