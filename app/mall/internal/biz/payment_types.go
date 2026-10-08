package biz

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
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
	ErrProviderTradeStateConflict    = errors.Conflict("PAYMENT_PROVIDER_TRADE_STATE_CONFLICT", "provider trade state conflicts with close")
	// ErrProviderOrderNotExist means the provider has no record of the trade:
	// the prepay never reached the provider or the order was purged. This alone
	// does not prove it is safe to release stock or invalidate signed links.
	ErrProviderOrderNotExist = errors.NotFound("PAYMENT_PROVIDER_ORDER_NOT_EXIST", "provider has no record of this trade")
	// ErrPaymentDescriptionTooLong: channels reject an over-long description
	// outright instead of truncating it.
	ErrPaymentDescriptionTooLong = errors.BadRequest("PAYMENT_DESCRIPTION_TOO_LONG", "payment description exceeds the channel limit")
)

// MaxPaymentDescriptionBytes is the strictest description length across the
// supported channels (Wechat 127 bytes; Alipay allows 256).
const MaxPaymentDescriptionBytes = 127

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

// SignedPaymentPayload records local parameter generation, not channel trade creation.
// ExpiresAt is the absolute deadline included in the signed channel request; zero
// means a legacy/unbounded action and must never authorize missing-trade closure.
type SignedPaymentPayload struct {
	Payload         string    `json:"payload"`
	ExpiresAt       time.Time `json:"channel_expires_at"`
	ProviderAccount string    `json:"provider_account"`
}

// Allow channel propagation and bounded clock skew before querying for absence.
const PaymentExpirySafetyMargin = time.Minute

type PaymentPrepayRequest struct {
	ExpiresAt   time.Time
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
	// ExpectedProviderAccount binds absence checks to the account/environment
	// that signed the action; a configuration change cannot prove trade absence.
	ExpectedProviderAccount string
	Method                  PaymentMethod
	OutTradeNo              string
	TransactionID           string
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
	// Rejection reports a definitive business rejection from the provider, as
	// opposed to a transient transport or system error that may be retried.
	// Only a Rejection may mark the local refund record as definitively failed.
	Rejection bool
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
	Purpose      RefundPurpose
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
	// Refund requests a refund identified by OutRefundNo. Implementations MUST
	// be idempotent per OutRefundNo: re-issuing a refund whose OutRefundNo was
	// already accepted by the provider must confirm the original result without
	// moving money again. Admin retries and the pending-refund reconciliation
	// worker both rely on this contract.
	Refund(context.Context, PaymentRefundRequest) (*PaymentRefundResult, error)
	ParseAndVerifyNotification(string, *http.Request) (*PaymentNotification, error)
	NotificationAck(string, bool) (PaymentNotificationAck, error)
}

type PaymentDO struct {
	ID, OrderID, UserID, MerchantID                                  int64
	Amount                                                           int64
	Currency, Status, Method, OutTradeNo, ThirdPartyTxID             string
	ReconciliationStatus, ReconciliationReason, ReconciliationDetail string
	PrepayLeaseToken, LastError                                      string
	PrepayLeaseUntil                                                 *time.Time
	PrepayAttempts                                                   int32
	ReconciliationVersion                                            int64
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
	// GetPaymentForJob bypasses caches so manual retries observe committed versions.
	GetPaymentForJob(context.Context, int64) (*PaymentDO, error)
	GetPaymentByUser(context.Context, int64, int64) (*PaymentDO, error)
	GetLatestPaymentByOrder(context.Context, int64) (*PaymentDO, error)
	GetActivePaymentByOrderMethod(context.Context, int64, string) (*PaymentDO, error)
	GetPaymentByOutTradeNo(context.Context, string) (*PaymentDO, error)
	GetOrderExpiry(context.Context, int64) (time.Time, error)
	BeginPaymentNotificationProcessing(context.Context, int64, string, string) (bool, error)
	RecordPaymentNotificationError(context.Context, int64, string) error
	MarkPaymentNotificationFailed(context.Context, int64, string) error
	ApplyPayQuery(context.Context, CheckPayArgs, *PaymentQueryResult) error
	MarkPayClosePending(context.Context, CheckPayArgs) error
	PreparePaymentRefund(context.Context, int64, string) (*PaymentDO, *PaymentRefund, error)
	RecordPaymentRefundError(context.Context, int64, string, bool) error
	ApplyPaymentRefund(context.Context, int64, int64) error
	ListStalePendingRefunds(context.Context, time.Duration, int) ([]PaymentRefund, error)
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
	EnqueueExpireOrder(context.Context, ExpireOrderArgs, time.Time) (*MQJob, error)
	EnqueueExpireOrderTx(context.Context, ExpireOrderArgs, time.Time) (*MQJob, error)
	GetMQJob(context.Context, int64) (*MQJob, error)
}

type PaymentJobUsecase interface {
	EnqueueCheckPay(context.Context, CheckPayArgs, time.Duration) (*MQJob, error)
	EnqueueCheckPayTx(context.Context, CheckPayArgs, time.Duration) (*MQJob, error)
	EnqueueClosePay(context.Context, ClosePayArgs, time.Duration) (*MQJob, error)
	EnqueueClosePayTx(context.Context, ClosePayArgs, time.Duration) (*MQJob, error)
	GetMQJob(context.Context, int64) (*MQJob, error)
}

type OrderExpiryRepo interface {
	ExpireOrder(context.Context, int64) error
	// ReapOverdueOrders re-enqueues expiry for pending_payment orders whose
	// payment window ended more than the given grace ago. It is the backstop
	// for expire_order jobs discarded after exhausting retries.
	ReapOverdueOrders(context.Context, time.Duration, int) ([]int64, error)
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
	ReconcilePendingRefunds(context.Context, time.Duration, int) (int, error)
	CreateCheckJob(context.Context, int64, int, time.Duration, time.Duration, string) (*MQJob, error)
	HandleNotification(context.Context, string, *http.Request) error
}
