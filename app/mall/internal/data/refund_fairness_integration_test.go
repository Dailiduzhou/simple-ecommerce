//go:build integration

package data

import (
	"fmt"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestPendingRefundSweepReachesRecoverable101stRecordIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	repo := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	var recoverableID int64
	var originalNumber string
	for i := range 101 {
		orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
		_, err := f.pool.Exec(f.ctx, "UPDATE orders SET status='paid',paid_payment_id=$2 WHERE id=$1", orderID, paymentID)
		require.NoError(t, err)
		_, refund, err := repo.PreparePaymentRefund(f.ctx, paymentID, fmt.Sprintf("%s_refund_%d", f.prefix, i))
		require.NoError(t, err)
		_, err = f.pool.Exec(f.ctx, "UPDATE order_refunds SET updated_at=now()-interval '2 hours'+$2*interval '1 millisecond' WHERE id=$1", refund.ID, i)
		require.NoError(t, err)
		if i < 100 {
			// Persist a valid plan, then change fulfillment so re-preparation
			// always rejects it before reaching the provider.
			_, err = f.pool.Exec(f.ctx, "UPDATE orders SET status='shipped' WHERE id=$1", orderID)
			require.NoError(t, err)
		} else {
			recoverableID, originalNumber = refund.ID, refund.OutRefundNo
		}
	}
	gateway := &integrationRefundGateway{f: f, t: t}
	uc := biz.NewPaymentUsecase(gateway, repo, nil, nil, nil, f.tx, integrationRefundIDs{}, log.DefaultLogger)
	settled, err := uc.ReconcilePendingRefunds(f.ctx, 10*time.Minute, 100)
	require.NoError(t, err)
	require.Zero(t, settled)
	require.Zero(t, gateway.calls)
	stale, err := repo.ListStalePendingRefunds(f.ctx, 10*time.Minute, 100)
	require.NoError(t, err)
	require.Len(t, stale, 1, "failed records must leave the next sweep's batch")
	require.Equal(t, recoverableID, stale[0].ID)
	settled, err = uc.ReconcilePendingRefunds(f.ctx, 10*time.Minute, 100)
	require.NoError(t, err)
	require.Equal(t, 1, settled)
	require.Equal(t, originalNumber, gateway.number)
	require.Equal(t, 1, gateway.transfers)
	var deferred int
	require.NoError(t, f.pool.QueryRow(f.ctx, "SELECT count(*) FROM order_refunds WHERE user_id=$1 AND status='pending' AND last_error<>'' AND updated_at>now()-interval '10 minutes'", f.userID).Scan(&deferred))
	require.Equal(t, 100, deferred)
	settled, err = uc.ReconcilePendingRefunds(f.ctx, 10*time.Minute, 100)
	require.NoError(t, err)
	require.Zero(t, settled)
	require.Equal(t, 1, gateway.calls, "completed refunds must not be sent again")
}
