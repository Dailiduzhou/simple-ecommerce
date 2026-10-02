package biz

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
)

func (uc *paymentUsecase) PrepayForOrder(ctx context.Context, args PrepayForOrderArgs) (*PrepayForOrderResult, error) {
	prepayRepo, ok := uc.paymentRepo.(PaymentPrepayRepo)
	if !ok {
		return nil, errors.ServiceUnavailable("PAYMENT_PREPAY_REPO_UNAVAILABLE", "payment prepay repository is unavailable")
	}
	method := args.Method.Normalize()
	if method.String() == "" {
		return nil, errors.BadRequest("PAYMENT_METHOD_REQUIRED", "payment method is required")
	}
	// Reject unsupported methods before creating any active payment row.
	capabilities, err := uc.gateway.Capabilities(method)
	if err != nil {
		return nil, err
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
	// Validate the channel description before any payment row exists: Wechat
	// caps it at 127 bytes and Alipay at 256, and both reject instead of
	// truncating.
	description := strings.TrimSpace(args.Description)
	if description == "" {
		description = fmt.Sprintf("Order %s", order.OutTradeNo)
	}
	if len(description) > MaxPaymentDescriptionBytes {
		return nil, ErrPaymentDescriptionTooLong
	}
	outTradeNo := uc.idGen.GenerateString()
	if err := validateOutTradeNo(outTradeNo); err != nil {
		return nil, err
	}
	payment, err := uc.paymentRepo.CreatePayment(ctx, CreatePaymentArgs{
		OrderID: order.ID, UserID: order.UserID, Amount: order.TotalAmount,
		Currency: order.Currency, Method: methodKey, OutTradeNo: outTradeNo,
	})
	if err != nil {
		return nil, err
	}
	if payment.Status == PaymentStatusPending && payment.Action.Type != "" {
		return &PrepayForOrderResult{Payment: payment, Prepay: &PaymentPrepayResult{ProviderReference: payment.ThirdPartyTxID, Action: payment.Action}}, nil
	}
	if payment.Status != PaymentStatusCreating {
		if payment.Status == PaymentStatusPending {
			// pending 却没有 action 属于适配器违约:支付已激活但无法把支付参数交给用户。
			uc.log.WithContext(ctx).Errorw("msg", "pending payment has no action", "payment_id", payment.ID, "method", payment.Method)
		}
		return nil, ErrPaymentStateConflict
	}
	leaseToken := uc.idGen.GenerateString()
	payment, err = prepayRepo.ClaimPaymentPrepay(ctx, payment.ID, leaseToken, uc.policy.PrepayLeaseDuration)
	if err != nil {
		return nil, err
	}
	if payment.Status == PaymentStatusPending && payment.Action.Type != "" {
		return &PrepayForOrderResult{
			Payment: payment,
			Prepay:  &PaymentPrepayResult{ProviderReference: payment.ThirdPartyTxID, Action: payment.Action},
		}, nil
	}
	prepay, err := uc.gateway.Prepay(ctx, PaymentPrepayRequest{
		Method: method, OutTradeNo: payment.OutTradeNo, Description: description,
		Amount: order.TotalAmount, Currency: order.Currency, ClientIP: args.ClientIP, Extension: args.Extension, ExpiresAt: order.ExpiresAt,
	})
	if err != nil {
		if recordErr := prepayRepo.RecordPaymentPrepayError(ctx, payment.ID, leaseToken, err.Error()); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record payment prepay error failed", "payment_id", payment.ID, "error", recordErr)
		}
		return nil, err
	}
	if prepay == nil || prepay.Action.Type == "" || len(prepay.Action.Payload) == 0 {
		err = errors.InternalServer("PAYMENT_PREPAY_RESPONSE_INVALID", "payment provider returned invalid prepay parameters")
		if recordErr := prepayRepo.RecordPaymentPrepayError(ctx, payment.ID, leaseToken, err.Error()); recordErr != nil {
			uc.log.WithContext(ctx).Errorw("msg", "record invalid payment prepay response failed", "payment_id", payment.ID, "error", recordErr)
		}
		return nil, err
	}

	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		activated, err := prepayRepo.FinalizePaymentPrepay(ctx, payment.ID, leaseToken, prepay.Action)
		if err != nil {
			return err
		}
		payment = activated
		if capabilities.RequiresPoll && uc.paymentJobs != nil {
			_, err = uc.paymentJobs.EnqueueCheckPayTx(ctx, CheckPayArgs{
				PaymentID: payment.ID, Provider: method.Provider, Trigger: "prepay",
				MaxPolls: uc.policy.PollMaxCount, PollIntervalSeconds: int(uc.policy.PollInterval.Seconds()),
				OrderExpiresAt: order.ExpiresAt,
			}, uc.policy.PollInitialDelay)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &PrepayForOrderResult{
		Payment: payment,
		Prepay:  &PaymentPrepayResult{ProviderReference: prepay.ProviderReference, Action: payment.Action},
	}, nil
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
