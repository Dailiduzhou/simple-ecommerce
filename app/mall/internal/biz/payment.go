package biz

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
)

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
	if stderrors.Is(err, ErrProviderTradeStateConflict) {
		query, queryErr := uc.gateway.Query(ctx, PaymentQueryRequest{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: payment.ThirdPartyTxID})
		if queryErr != nil {
			return nil, queryErr
		}
		if query != nil && query.TradeState.IsTerminal() {
			if applyErr := uc.paymentRepo.ApplyPayQuery(ctx, closeArgs, query); applyErr != nil {
				return nil, applyErr
			}
			return &PaymentCloseResult{Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: query.TransactionID, Success: query.TradeState == TradeStateClosed}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("payment provider returned an empty close result")
	}
	if result.Success {
		err = uc.paymentRepo.ApplyPayQuery(ctx, CheckPayArgs{PaymentID: payment.ID, Provider: method.Provider, Trigger: "api_close"}, &PaymentQueryResult{
			Method: method, OutTradeNo: payment.OutTradeNo, TransactionID: result.TransactionID,
			TradeState: TradeStateClosed, Amount: payment.Amount, Currency: payment.Currency,
		})
	}
	return result, err
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
