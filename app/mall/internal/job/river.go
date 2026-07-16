package job

import (
	"context"
	"encoding/json"
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
		return river.JobCancel(fmt.Errorf("payment_id and provider are required"))
	}
	if w.paymentGateway == nil || w.paymentRepo == nil {
		return river.JobCancel(fmt.Errorf("payment worker dependencies are missing"))
	}
	payment, err := w.paymentRepo.GetPayment(ctx, args.PaymentID)
	if err != nil {
		return err
	}
	method, err := biz.ParsePaymentMethod(payment.Method)
	if err != nil {
		return river.JobCancel(err)
	}
	if method.Provider != args.Provider {
		return river.JobCancel(fmt.Errorf("job provider does not match payment method"))
	}

	result, err := w.paymentGateway.Query(ctx, biz.PaymentQueryRequest{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID})
	if err != nil {
		observability.PaymentReconcileJob(ctx, args.Provider, "technical_error")
		return err
	}
	if result.TradeState.IsTerminal() {
		err := w.paymentRepo.ApplyPayQuery(ctx, args, result)
		if err != nil {
			observability.PaymentReconcileJob(ctx, args.Provider, "apply_error")
		} else {
			observability.PaymentReconcileJob(ctx, args.Provider, "terminal")
		}
		return err
	}
	if !result.TradeState.IsPending() {
		return fmt.Errorf("provider returned unsupported state %s", result.TradeState)
	}

	state := pollOutput{PollCount: args.PollCount}
	if output := job.Output(); len(output) > 0 {
		_ = json.Unmarshal(output, &state)
	}
	state.PollCount++
	args.PollCount = state.PollCount
	if err := river.RecordOutput(ctx, state); err != nil {
		return err
	}
	if state.PollCount < args.MaxPolls {
		observability.PaymentReconcileJob(ctx, args.Provider, "pending")
		return river.JobSnooze(time.Duration(args.PollIntervalSeconds) * time.Second)
	}

	if err := w.paymentRepo.MarkPayClosePending(ctx, args); err != nil {
		return err
	}
	capabilities, err := w.paymentGateway.Capabilities(method)
	if err != nil {
		return err
	}
	if !capabilities.SupportsClose {
		return w.paymentRepo.MarkReconciliationRequired(ctx, biz.ReconciliationFailure{PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, job.Attempt), LastError: "business polling deadline reached and provider cannot close"})
	}
	closed, err := w.paymentGateway.Close(ctx, biz.PaymentCloseRequest{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID})
	if err != nil {
		return err
	}
	if !closed.Success {
		return fmt.Errorf("provider did not confirm payment close")
	}
	return w.paymentRepo.ApplyPayQuery(ctx, args, &biz.PaymentQueryResult{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: closed.TransactionID, TradeState: biz.TradeStateClosed, Amount: payment.Amount, Currency: payment.Currency})
}

func (w *CheckPayWorker) NextRetry(job *river.Job[biz.CheckPayArgs]) time.Time {
	// Only technical errors reach River retry. Business pending uses JobSnooze.
	backoff := time.Second << min(job.Attempt, 6)
	return time.Now().Add(backoff)
}
