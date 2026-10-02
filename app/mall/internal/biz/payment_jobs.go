package biz

import (
	"context"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
)

const CheckPayJobKind = "check_pay"

const ClosePayJobKind = "close_pay"

const ReconcileRefundsJobKind = "reconcile_refunds"

type ReconcileRefundsArgs struct{}

func (ReconcileRefundsArgs) Kind() string { return ReconcileRefundsJobKind }

type CheckPayArgs struct {
	PaymentID           int64  `json:"payment_id" river:"unique"`
	Provider            string `json:"provider" river:"unique"`
	NotificationID      int64  `json:"notification_id" river:"unique"`
	Trigger             string `json:"trigger"`
	PollCount           int    `json:"poll_count"`
	MaxPolls            int    `json:"max_polls"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	// OrderExpiresAt bounds poll-triggered close: closing earlier would cancel
	// an order that is still inside its payment window. Zero means the enqueuer
	// could not supply the deadline and legacy close-on-exhaustion applies.
	OrderExpiresAt time.Time `json:"order_expires_at"`
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
