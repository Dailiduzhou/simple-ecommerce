package biz

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
)

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
