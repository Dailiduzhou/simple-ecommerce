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
	PaymentStatusSuccess           = "success"
	PaymentStatusRefunded          = "refunded"
	PaymentStatusClosePending      = "close_pending"
	PaymentStatusClosed            = "closed"
	PaymentStatusReconcileRequired = "reconcile_required"
)

var (
	ErrPaymentConflict            = errors.Conflict("PAYMENT_CONFLICT", "an active payment already exists")
	ErrPaymentNotFound            = errors.NotFound("PAYMENT_NOT_FOUND", "payment not found")
	ErrPaymentStateConflict       = errors.Conflict("PAYMENT_STATE_CONFLICT", "payment state transition conflicts with the current state")
	ErrPaymentProviderUnavailable = errors.ServiceUnavailable("PAYMENT_PROVIDER_NOT_AVAILABLE", "payment provider is not available")
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
	case TradeStateSuccess, TradeStateRefund, TradeStateClosed, TradeStateRevoked:
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

type PaymentAdapter interface {
	Provider() string
	Supports(method PaymentMethod) bool
	Capabilities(method PaymentMethod) PaymentCapabilities
	Prepay(context.Context, PaymentPrepayRequest) (*PaymentPrepayResult, error)
	Query(context.Context, PaymentQueryRequest) (*PaymentQueryResult, error)
	Close(context.Context, PaymentCloseRequest) (*PaymentCloseResult, error)
	ParseAndVerifyNotification(*http.Request) (*PaymentNotification, error)
	NotificationAck(success bool) PaymentNotificationAck
}

type PaymentGateway interface {
	Capabilities(PaymentMethod) (PaymentCapabilities, error)
	Prepay(context.Context, PaymentPrepayRequest) (*PaymentPrepayResult, error)
	Query(context.Context, PaymentQueryRequest) (*PaymentQueryResult, error)
	Close(context.Context, PaymentCloseRequest) (*PaymentCloseResult, error)
	ParseAndVerifyNotification(string, *http.Request) (*PaymentNotification, error)
	NotificationAck(string, bool) (PaymentNotificationAck, error)
}

const CheckPayJobKind = "check_pay"

type CheckPayArgs struct {
	PaymentID           int64  `json:"payment_id" river:"unique"`
	Provider            string `json:"provider" river:"unique"`
	NotificationID      int64  `json:"notification_id"`
	Trigger             string `json:"trigger"`
	PollCount           int    `json:"poll_count"`
	MaxPolls            int    `json:"max_polls"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

func (CheckPayArgs) Kind() string { return CheckPayJobKind }

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
	ID, OrderID, UserID, MerchantID                      int64
	Amount                                               int64
	Currency, Status, Method, OutTradeNo, ThirdPartyTxID string
	Action                                               PaymentAction
	PaidAt                                               *time.Time
	CreatedAt, UpdatedAt                                 time.Time
}

type CreatePaymentArgs struct {
	OrderID, UserID, MerchantID  int64
	Amount                       int64
	Currency, Method, OutTradeNo string
}

type ReconciliationFailure struct {
	PaymentID  int64
	Provider   string
	RiverJobID *int64
	Attempt    int
	LastError  string
}

type PaymentRepo interface {
	CreatePayment(context.Context, CreatePaymentArgs) (*PaymentDO, error)
	MarkPaymentPending(context.Context, int64, PaymentAction) (*PaymentDO, error)
	GetPayment(context.Context, int64) (*PaymentDO, error)
	GetPaymentByUser(context.Context, int64, int64) (*PaymentDO, error)
	GetLatestPaymentByOrder(context.Context, int64) (*PaymentDO, error)
	GetActivePaymentByOrderMethod(context.Context, int64, string) (*PaymentDO, error)
	GetPaymentByOutTradeNo(context.Context, string) (*PaymentDO, error)
	ApplyPayQuery(context.Context, CheckPayArgs, *PaymentQueryResult) error
	MarkPayClosePending(context.Context, CheckPayArgs) error
	MarkReconciliationRequired(context.Context, ReconciliationFailure) error
	RecordReconciliationFailure(context.Context, ReconciliationFailure) error
}

type PaymentNotificationRepo interface {
	PersistAndEnqueueNotification(context.Context, *PaymentNotification, CheckPayArgs) (bool, error)
}

type PaymentMQRepo interface {
	EnqueueCheckPay(context.Context, CheckPayArgs, time.Time) (*MQJob, error)
	EnqueueCheckPayTx(context.Context, CheckPayArgs, time.Time) (*MQJob, error)
	GetMQJob(context.Context, int64) (*MQJob, error)
}

type PaymentJobUsecase interface {
	EnqueueCheckPay(context.Context, CheckPayArgs, time.Duration) (*MQJob, error)
	EnqueueCheckPayTx(context.Context, CheckPayArgs, time.Duration) (*MQJob, error)
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
	log              *log.Helper
}

func NewPaymentUsecase(gateway PaymentGateway, paymentRepo PaymentRepo, notificationRepo PaymentNotificationRepo, orderRepo OrderRepo, paymentJobs PaymentJobUsecase, tx TxManager, idGen IDGenerator, logger log.Logger) PaymentUsecase {
	return &paymentUsecase{gateway: gateway, paymentRepo: paymentRepo, notificationRepo: notificationRepo, orderRepo: orderRepo, paymentJobs: paymentJobs, tx: tx, idGen: idGen, log: log.NewHelper(logger)}
}

func (uc *paymentUsecase) PrepayForOrder(ctx context.Context, args PrepayForOrderArgs) (*PrepayForOrderResult, error) {
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
	payment, err := uc.paymentRepo.GetActivePaymentByOrderMethod(ctx, order.ID, methodKey)
	if err != nil && !errors.Is(err, ErrPaymentNotFound) {
		return nil, err
	}
	if payment == nil {
		outTradeNo := uc.idGen.GenerateString()
		if err := validateOutTradeNo(outTradeNo); err != nil {
			return nil, err
		}
		payment, err = uc.paymentRepo.CreatePayment(ctx, CreatePaymentArgs{
			OrderID: order.ID, UserID: order.UserID, Amount: order.TotalAmount,
			Currency: order.Currency, Method: methodKey, OutTradeNo: outTradeNo,
		})
		if err != nil {
			return nil, err
		}
	}
	if payment.Status == PaymentStatusPending && payment.Action.Type != "" {
		return &PrepayForOrderResult{Payment: payment, Prepay: &PaymentPrepayResult{ProviderReference: payment.ThirdPartyTxID, Action: payment.Action}}, nil
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
		return nil, err
	}

	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		activated, err := uc.paymentRepo.MarkPaymentPending(ctx, payment.ID, prepay.Action)
		if err != nil {
			return err
		}
		payment = activated
		if capabilities.RequiresPoll && uc.paymentJobs != nil {
			_, err = uc.paymentJobs.EnqueueCheckPayTx(ctx, CheckPayArgs{
				PaymentID: payment.ID, Provider: method.Provider, Trigger: "prepay", MaxPolls: 30, PollIntervalSeconds: 10,
			}, 5*time.Second)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &PrepayForOrderResult{Payment: payment, Prepay: prepay}, nil
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
	closeArgs := CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "api_close", MaxPolls: 1, PollIntervalSeconds: 1}
	if err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.paymentRepo.MarkPayClosePending(ctx, closeArgs); err != nil {
			return err
		}
		_, err := uc.paymentJobs.EnqueueCheckPayTx(ctx, closeArgs, 0)
		return err
	}); err != nil {
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
	_, err = uc.notificationRepo.PersistAndEnqueueNotification(ctx, notification, CheckPayArgs{
		PaymentID: payment.ID, Provider: method.Provider, Trigger: "callback", MaxPolls: 30, PollIntervalSeconds: 10,
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
