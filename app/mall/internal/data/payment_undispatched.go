package data

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgtype"
)

// recoverUnsupportedPayments runs only with the order and its payments locked.
// Historical capability failures happened before ClaimPaymentPrepay incremented
// attempts. Never infer non-dispatch from a missing action alone: the channel
// could have accepted a request before the application crashed.
func recoverUnsupportedPayments(ctx context.Context, q db.Querier, data *Data, logger *log.Helper, payments []db.Payment) error {
	for i, payment := range payments {
		if !unsupportedPaymentNeverDispatched(payment) {
			continue
		}
		failed, err := q.MarkPaymentFailed(ctx, db.MarkPaymentFailedParams{
			ID: payment.ID, LastError: pgtype.Text{String: "unsupported payment method; prepay was never claimed", Valid: true},
		})
		if err != nil {
			return err
		}
		payments[i] = failed
		(&PaymentRepo{data: data, log: logger}).invalidatePayment(ctx, failed)
	}
	return nil
}

func unsupportedPaymentNeverDispatched(p db.Payment) bool {
	if (p.Status != biz.PaymentStatusCreating && p.Status != biz.PaymentStatusClosePending) ||
		p.PrepayAttempts != 0 || p.PrepayLeaseToken.Valid || p.PrepayLeaseUntil.Valid ||
		p.ActionType.Valid || (len(p.ActionPayload) > 0 && string(p.ActionPayload) != "null") || p.ThirdPartyTxID.Valid ||
		(p.ReconciliationStatus != "" && p.ReconciliationStatus != biz.ReconciliationStatusNone) {
		return false
	}
	// Frozen historical capability set, not the currently enabled providers.
	// Disabling a valid provider must never discard an in-flight payment.
	switch p.PayChannel {
	case "wechat:jsapi", "wechat:native", "wechat:app", "alipay:app", "alipay:wap":
		return false
	default:
		return true
	}
}
