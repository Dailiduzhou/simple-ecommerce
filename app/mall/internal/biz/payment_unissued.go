package biz

import "time"

// CanConfirmUnissuedPayment is valid only after the creating state is fenced
// off. APP/WAP sign locally and never return an action until finalize commits;
// a late signer therefore cannot issue an action from close_pending. A lease
// expiring is NOT evidence that a network-based prepay was never dispatched.
// Settlement must repeat this check on the locked row, not trust a snapshot.
func CanConfirmUnissuedPayment(p *PaymentDO, now time.Time) bool {
	if p.Status != PaymentStatusClosePending && p.Status != PaymentStatusClosed {
		return false
	}
	if p.Action.Type != "" || len(p.Action.Payload) != 0 || p.ThirdPartyTxID != "" {
		return false
	}
	local := p.Method == "alipay:app" || p.Method == "alipay:wap"
	if local {
		return p.PrepayLeaseUntil == nil || !p.PrepayLeaseUntil.After(now)
	}
	return p.PrepayAttempts == 0 && p.PrepayLeaseUntil == nil && p.PrepayLeaseToken == ""
}
