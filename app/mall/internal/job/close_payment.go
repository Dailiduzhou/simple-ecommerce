package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/riverqueue/river"
)

type ClosePayWorker struct {
	river.WorkerDefaults[biz.ClosePayArgs]
	gateway biz.PaymentGateway
	repo    biz.PaymentRepo
}

func NewClosePayWorker(gateway biz.PaymentGateway, repo biz.PaymentRepo) *ClosePayWorker {
	return &ClosePayWorker{gateway: gateway, repo: repo}
}

func (w *ClosePayWorker) Work(ctx context.Context, job *river.Job[biz.ClosePayArgs]) error {
	args := job.Args
	if args.PaymentID <= 0 || args.Provider == "" || w.gateway == nil || w.repo == nil {
		return river.JobCancel(fmt.Errorf("close_pay requires payment_id, provider, gateway, and repository"))
	}
	payment, err := w.repo.GetPayment(ctx, args.PaymentID)
	if err != nil {
		return err
	}
	if payment == nil {
		return fmt.Errorf("payment repository returned an empty payment")
	}
	method, err := biz.ParsePaymentMethod(payment.Method)
	if err != nil {
		return river.JobCancel(err)
	}
	if method.Provider != args.Provider {
		return river.JobCancel(fmt.Errorf("close_pay provider does not match payment"))
	}
	query, err := w.gateway.Query(ctx, biz.PaymentQueryRequest{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID,
	})
	applyArgs := biz.CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "close_pay"}
	if errors.Is(err, biz.ErrProviderOrderNotExist) {
		// "Order not exist" only proves the trade never reached the provider
		// when prepay never finalized (no action recorded). A payment that did
		// complete prepay must be visible to the provider — if it is not, the
		// query may have hit the wrong merchant account or environment, and
		// auto-closing could discard money the user actually paid. Reconcile.
		if payment.Action.Type == "" {
			return w.repo.ApplyPayQuery(ctx, applyArgs, &biz.PaymentQueryResult{
				Method: method, OutTradeNo: payment.OutTradeNo,
				TradeState: biz.TradeStateClosed, Amount: payment.Amount, Currency: payment.Currency,
			})
		}
		return w.repo.MarkReconciliationRequired(ctx, biz.ReconciliationFailure{
			PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, job.Attempt),
			Reason:    "provider_order_not_exist",
			LastError: "provider has no record of a payment whose prepay was finalized; possible gateway misconfiguration",
		})
	}
	if err != nil {
		return err
	}
	if query == nil {
		return fmt.Errorf("payment provider returned an empty query result")
	}
	if query.TradeState.IsTerminal() {
		return w.repo.ApplyPayQuery(ctx, applyArgs, query)
	}
	capabilities, err := w.gateway.Capabilities(method)
	if err != nil {
		return err
	}
	if !capabilities.SupportsClose {
		return w.repo.MarkReconciliationRequired(ctx, biz.ReconciliationFailure{
			PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, job.Attempt),
			Reason: "close_failed", LastError: "provider does not support payment close",
		})
	}
	closed, err := w.gateway.Close(ctx, biz.PaymentCloseRequest{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID,
	})
	if err != nil {
		return err
	}
	if closed == nil {
		return fmt.Errorf("payment provider returned an empty close result")
	}
	if !closed.Success {
		return fmt.Errorf("provider did not confirm payment close")
	}
	return w.repo.ApplyPayQuery(ctx, applyArgs, &biz.PaymentQueryResult{
		Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: closed.TransactionID,
		TradeState: biz.TradeStateClosed, Amount: payment.Amount, Currency: payment.Currency,
	})
}

func (w *ClosePayWorker) NextRetry(job *river.Job[biz.ClosePayArgs]) time.Time {
	return time.Now().Add(time.Second << min(job.Attempt, 6))
}
