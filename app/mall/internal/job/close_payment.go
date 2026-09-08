package job

import (
	"context"
	"encoding/json"
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
	var signed biz.SignedPaymentPayload
	localSigning := method.Provider == "alipay" && (method.Product == "app" || method.Product == "wap") && payment.Action.Type != ""
	if localSigning {
		localSigning = json.Unmarshal(payment.Action.Payload, &signed) == nil
	}
	queryRequest := biz.PaymentQueryRequest{Method: method, OutTradeNo: payment.OutTradeNo,
		TransactionID: payment.ThirdPartyTxID, ExpectedProviderAccount: signed.ProviderAccount}
	queryStarted := time.Now()
	query, err := w.gateway.Query(ctx, queryRequest)
	applyArgs := biz.CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "close_pay"}
	if errors.Is(err, biz.ErrProviderOrderNotExist) {
		// APP/WAP only generate signed parameters locally. Absence is normal,
		// but query only counts as closure evidence after those parameters expire.
		safeAbsent := payment.Status == biz.PaymentStatusClosePending && payment.Action.Type == "" && payment.PrepayAttempts == 0 && payment.PrepayLeaseUntil == nil && payment.ThirdPartyTxID == ""
		if localSigning {
			if signed.ProviderAccount != "" && signed.Payload != "" && !signed.ExpiresAt.IsZero() {
				safeAt := signed.ExpiresAt.Add(biz.PaymentExpirySafetyMargin)
				if queryStarted.Before(safeAt) {
					// Snoozing does not exhaust River attempts; re-query AFTER cutoff.
					return river.JobSnooze(max(time.Second, time.Until(safeAt)))
				}
				safeAbsent = true
			}
		}
		if safeAbsent {
			return w.repo.ApplyPayQuery(ctx, applyArgs, &biz.PaymentQueryResult{
				Method: method, OutTradeNo: payment.OutTradeNo,
				TradeState: biz.TradeStateClosed, Amount: payment.Amount, Currency: payment.Currency,
			})
		}
		return w.repo.MarkReconciliationRequired(ctx, biz.ReconciliationFailure{
			PaymentID: payment.ID, Provider: method.Provider, Attempt: max(1, job.Attempt),
			Reason:    "provider_order_not_exist",
			LastError: "provider absence without proof that previously issued payment parameters are unusable",
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
	if errors.Is(err, biz.ErrProviderTradeStateConflict) {
		// Payment may have won between the initial query and the close request.
		query, queryErr := w.gateway.Query(ctx, queryRequest)
		if queryErr != nil {
			return queryErr
		}
		if query != nil && query.TradeState.IsTerminal() {
			return w.repo.ApplyPayQuery(ctx, applyArgs, query)
		}
	}
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
