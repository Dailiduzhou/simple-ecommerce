package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
)

type CheckPayWorker struct {
	river.WorkerDefaults[biz.CheckPayArgs]
	paymentGateway biz.PaymentGateway
	paymentRepo    biz.PaymentRepo
	log            *log.Helper
}

func NewCheckPayWorker(gateway biz.PaymentGateway, repo biz.PaymentRepo, logger log.Logger) *CheckPayWorker {
	return &CheckPayWorker{paymentGateway: gateway, paymentRepo: repo, log: log.NewHelper(logger)}
}

type pollOutput struct {
	PollCount int `json:"poll_count"`
}

func (w *CheckPayWorker) Work(ctx context.Context, job *river.Job[biz.CheckPayArgs]) error {
	args := biz.NormalizeCheckPayArgs(job.Args)
	if args.PaymentID <= 0 || args.Provider == "" {
		return w.cancel(ctx, args, fmt.Errorf("payment_id and provider are required"))
	}
	if w.paymentGateway == nil || w.paymentRepo == nil {
		return w.cancel(ctx, args, fmt.Errorf("payment worker dependencies are missing"))
	}
	payment, err := w.paymentRepo.GetPayment(ctx, args.PaymentID)
	if err != nil {
		return err
	}
	if payment == nil {
		return w.retryable(ctx, args, fmt.Errorf("payment repository returned an empty payment"))
	}
	method, err := biz.ParsePaymentMethod(payment.Method)
	if err != nil {
		return w.cancel(ctx, args, err)
	}
	if method.Provider != args.Provider {
		return w.cancel(ctx, args, fmt.Errorf("job provider does not match payment method"))
	}
	proceed, err := w.paymentRepo.BeginPaymentNotificationProcessing(ctx, args.NotificationID, args.Provider, payment.OutTradeNo)
	if err != nil {
		if errors.Is(err, biz.ErrPaymentNotificationBinding) {
			return w.cancel(ctx, args, err)
		}
		return err
	}
	if !proceed {
		return nil
	}

	result, err := w.paymentGateway.Query(ctx, biz.PaymentQueryRequest{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID})
	if err != nil {
		observability.PaymentReconcileJob(ctx, args.Provider, "technical_error")
		return w.retryable(ctx, args, err)
	}
	if result == nil {
		return w.retryable(ctx, args, fmt.Errorf("payment provider returned an empty query result"))
	}
	if result.TradeState.IsTerminal() {
		err := w.paymentRepo.ApplyPayQuery(ctx, args, result)
		if err != nil {
			observability.PaymentReconcileJob(ctx, args.Provider, "apply_error")
		} else {
			observability.PaymentReconcileJob(ctx, args.Provider, "terminal")
		}
		if err != nil {
			return w.retryable(ctx, args, err)
		}
		return nil
	}
	if !result.TradeState.IsPending() {
		return w.retryable(ctx, args, fmt.Errorf("provider returned unsupported state %s", result.TradeState))
	}

	state := pollOutput{PollCount: args.PollCount}
	if output := job.Output(); len(output) > 0 {
		_ = json.Unmarshal(output, &state)
	}
	state.PollCount++
	args.PollCount = state.PollCount
	if err := river.RecordOutput(ctx, state); err != nil {
		return w.retryable(ctx, args, err)
	}
	if state.PollCount < args.MaxPolls {
		observability.PaymentReconcileJob(ctx, args.Provider, "pending")
		return river.JobSnooze(time.Duration(args.PollIntervalSeconds) * time.Second)
	}

	if err := w.paymentRepo.MarkPayClosePending(ctx, args); err != nil {
		return w.retryable(ctx, args, err)
	}
	return nil
}

func (w *CheckPayWorker) retryable(ctx context.Context, args biz.CheckPayArgs, workErr error) error {
	if w.paymentRepo == nil || args.NotificationID <= 0 {
		return workErr
	}
	if err := w.paymentRepo.RecordPaymentNotificationError(ctx, args.NotificationID, workErr.Error()); err != nil {
		return fmt.Errorf("%w; record payment notification error: %v", workErr, err)
	}
	return workErr
}

func (w *CheckPayWorker) cancel(ctx context.Context, args biz.CheckPayArgs, workErr error) error {
	if w.paymentRepo != nil && args.NotificationID > 0 {
		if err := w.paymentRepo.MarkPaymentNotificationFailed(ctx, args.NotificationID, workErr.Error()); err != nil {
			return fmt.Errorf("mark payment notification failed: %w", err)
		}
	}
	return river.JobCancel(workErr)
}

func (w *CheckPayWorker) NextRetry(job *river.Job[biz.CheckPayArgs]) time.Time {
	// Only technical errors reach River retry. Business pending uses JobSnooze.
	backoff := time.Second << min(job.Attempt, 6)
	return time.Now().Add(backoff)
}
