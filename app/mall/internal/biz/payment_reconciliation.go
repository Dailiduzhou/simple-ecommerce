package biz

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
)

const (
	ReconciliationActionRetry   = "retry"
	ReconciliationActionResolve = "resolve"
)

var ErrReconciliationConflict = errors.Conflict("RECONCILIATION_CONFLICT", "reconciliation changed or unresolved financial facts remain; reload the case")

type ReconciliationInput struct {
	PaymentID, ExpectedVersion               int64
	Action, IdempotencyKey, Reason, Evidence string
}

func requireReconciliationAdmin(actor Actor) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if !actor.Admin {
		return errors.Forbidden("FORBIDDEN", "reconciliation requires an administrator")
	}
	return nil
}

func (in ReconciliationInput) Validate(actor Actor) error {
	if err := requireReconciliationAdmin(actor); err != nil {
		return err
	}
	if in.PaymentID <= 0 || in.ExpectedVersion <= 0 || (in.Action != ReconciliationActionRetry && in.Action != ReconciliationActionResolve) ||
		len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 64 || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey ||
		strings.TrimSpace(in.Reason) == "" || utf8.RuneCountInString(in.Reason) > 255 || utf8.RuneCountInString(in.Evidence) > 2000 ||
		(in.Action == ReconciliationActionResolve && strings.TrimSpace(in.Evidence) == "") {
		return errors.BadRequest("RECONCILIATION_INVALID", "positive payment/version, action, idempotency key, reason and resolution evidence are required")
	}
	return nil
}

type ReconciliationAction struct {
	ID, PaymentID, ActorID, FromVersion, ToVersion                 int64
	Action, FromStatus, ToStatus, IdempotencyKey, Reason, Evidence string
	JobID                                                          int64
	CreatedAt                                                      time.Time
}

type ReconciliationCase struct {
	Payment *PaymentDO
	Detail  string
}

type PaymentReconciliationRepo interface {
	ApplyReconciliation(context.Context, Actor, ReconciliationInput) (*ReconciliationAction, error)
	ListReconciliationCases(context.Context, int64, int32) ([]ReconciliationCase, error)
	ListReconciliationActions(context.Context, int64, int64, int32) ([]ReconciliationAction, error)
}

type PaymentReconciliationUsecase struct{ repo PaymentReconciliationRepo }

func NewPaymentReconciliationUsecase(repo PaymentReconciliationRepo) *PaymentReconciliationUsecase {
	return &PaymentReconciliationUsecase{repo: repo}
}
func (uc *PaymentReconciliationUsecase) Apply(ctx context.Context, actor Actor, input ReconciliationInput) (*ReconciliationAction, error) {
	if err := input.Validate(actor); err != nil {
		return nil, err
	}
	return uc.repo.ApplyReconciliation(ctx, actor, input)
}
func (uc *PaymentReconciliationUsecase) ListCases(ctx context.Context, actor Actor, afterID int64, limit int32) ([]ReconciliationCase, error) {
	if err := requireReconciliationAdmin(actor); err != nil {
		return nil, err
	}
	if afterID < 0 || limit < 0 || limit > 100 {
		return nil, errors.BadRequest("RECONCILIATION_PAGE_INVALID", "after_id must be nonnegative and page_size at most 100")
	}
	if limit == 0 {
		limit = 20
	}
	return uc.repo.ListReconciliationCases(ctx, afterID, limit)
}
func (uc *PaymentReconciliationUsecase) ListActions(ctx context.Context, actor Actor, paymentID, afterID int64, limit int32) ([]ReconciliationAction, error) {
	if err := requireReconciliationAdmin(actor); err != nil {
		return nil, err
	}
	if paymentID <= 0 || afterID < 0 || limit < 0 || limit > 100 {
		return nil, errors.BadRequest("RECONCILIATION_PAGE_INVALID", "payment_id must be positive, after_id nonnegative and page_size at most 100")
	}
	if limit == 0 {
		limit = 20
	}
	return uc.repo.ListReconciliationActions(ctx, paymentID, afterID, limit)
}

// Resolving an operational case acknowledges verified, already-settled facts.
// It never writes payment status, refunds or inventory to fit an operator input.
func CanResolveReconciliation(payment *PaymentDO, order Order, refund *PaymentRefund) bool {
	if payment == nil || payment.OrderID != order.ID || payment.UserID != order.UserID || payment.Amount != order.TotalAmount || payment.Currency != order.Currency {
		return false
	}
	// Provider-side refunds cannot be acknowledged using the original charge.
	// Only a matching, successfully settled local refund can clear this case.
	if payment.ReconciliationReason == "provider_side_refund" && payment.Status != PaymentStatusRefunded {
		return false
	}
	funded := order.Status == OrderStatusPaid || order.Status == OrderStatusShipped || order.Status == OrderStatusCompleted
	if refund != nil && refund.Status == PaymentRefundStatusPending {
		return false
	}
	switch payment.Status {
	case PaymentStatusSuccess:
		return funded && order.PaidPaymentID == payment.ID && payment.ThirdPartyTxID != ""
	case PaymentStatusRefunded:
		if refund == nil || refund.PaymentID != payment.ID || refund.OrderID != order.ID || refund.UserID != payment.UserID || refund.Status != PaymentRefundStatusSuccess || refund.TotalAmount != payment.Amount || refund.RefundAmount != payment.Amount || refund.Currency != payment.Currency {
			return false
		}
		switch refund.Purpose {
		case RefundOrderCancel:
			return order.Status == OrderStatusRefunded && order.PaidPaymentID == payment.ID
		case RefundLate:
			return order.Status == OrderStatusCancelled && order.PaidPaymentID == 0
		case RefundDuplicate:
			return (funded || order.Status == OrderStatusRefunded) && order.PaidPaymentID > 0 && order.PaidPaymentID != payment.ID
		}
	case PaymentStatusClosed, PaymentStatusFailed:
		// Clearing the review allows normal expiry/cancellation to release an
		// unpaid order. Requiring expiry first would deadlock those operations,
		// which intentionally wait for the review to finish.
		return (order.Status == OrderStatusCancelled && order.PaidPaymentID == 0) ||
			(order.Status == OrderStatusRefunded && order.PaidPaymentID > 0 && order.PaidPaymentID != payment.ID) ||
			(funded && order.PaidPaymentID > 0 && order.PaidPaymentID != payment.ID) ||
			(order.Status == OrderStatusPendingPayment && order.PaidPaymentID == 0)
	}
	return false
}

func ReconciliationNeedsReview(status string) bool {
	return status == ReconciliationStatusRequired || status == ReconciliationStatusProcessing
}

// ReconciliationJobCurrent fences manual retries, not ordinary polling or new
// provider facts. A retry only belongs to the processing version that queued it.
func ReconciliationJobCurrent(jobVersion, currentVersion int64, status string) bool {
	return jobVersion == 0 || (jobVersion == currentVersion && status == ReconciliationStatusProcessing)
}
