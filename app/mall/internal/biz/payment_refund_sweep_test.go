package biz

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type refundSweepRepo struct {
	paymentTestRepo
	failurePhase                          string
	cursors, attempted, settled, recorded []int64
}

func (r *refundSweepRepo) ListStalePendingRefunds(ctx context.Context, age time.Duration, limit int, afterID int64) ([]PaymentRefund, error) {
	r.cursors = append(r.cursors, afterID)
	return r.paymentTestRepo.ListStalePendingRefunds(ctx, age, limit, afterID)
}
func (r *refundSweepRepo) GetPayment(_ context.Context, id int64) (*PaymentDO, error) {
	return &PaymentDO{ID: id, OrderID: id, UserID: 42, Method: "alipay:wap", Status: PaymentStatusSuccess, Amount: 100, Currency: "CNY", OutTradeNo: fmt.Sprintf("pay_%d", id)}, nil
}
func (r *refundSweepRepo) PreparePaymentRefund(ctx context.Context, id int64, number string) (*PaymentDO, *PaymentRefund, error) {
	r.attempted = append(r.attempted, id)
	if r.failurePhase == "prepare" && id <= 100 {
		return nil, nil, ErrPaymentStateConflict
	}
	p, _ := r.GetPayment(ctx, id)
	return p, &PaymentRefund{ID: id, PaymentID: id, OrderID: id, UserID: 42, OutRefundNo: number, RefundAmount: 100, Currency: "CNY", Status: PaymentRefundStatusPending}, nil
}
func (r *refundSweepRepo) RecordPaymentRefundError(_ context.Context, id int64, _ string, definitive bool) error {
	if definitive {
		return fmt.Errorf("sweep must not make an uncertain refund definitive")
	}
	r.recorded = append(r.recorded, id)
	return nil
}

func (r *refundSweepRepo) ApplyPaymentRefund(_ context.Context, id, refundID int64) error {
	if r.failurePhase == "apply" && id <= 100 {
		return ErrPaymentStateConflict
	}
	r.settled = append(r.settled, id)
	return nil
}

func TestRefundSweepAdvancesBeyondFailedFirstPage(t *testing.T) {
	for _, phase := range []string{"prepare", "apply"} {
		t.Run(phase, func(t *testing.T) {
			repo := &refundSweepRepo{failurePhase: phase}
			for id := int64(1); id <= 101; id++ {
				repo.staleRefunds = append(repo.staleRefunds, PaymentRefund{ID: id, PaymentID: id, OutRefundNo: fmt.Sprintf("refund_%d", id)})
			}
			gateway := &paymentTestGateway{capabilities: PaymentCapabilities{SupportsRefund: true}, refundResult: &PaymentRefundResult{Success: true}}
			uc := NewPaymentUsecase(gateway, repo, nil, nil, nil, nil, nil, log.DefaultLogger)
			settled, err := uc.ReconcilePendingRefunds(context.Background(), time.Minute, 100)
			require.NoError(t, err)
			require.Equal(t, 1, settled)
			require.Equal(t, []int64{0, 100}, repo.cursors)
			require.Len(t, repo.attempted, 101)
			require.Len(t, repo.recorded, 100)
			require.Equal(t, []int64{101}, repo.settled)
			require.Equal(t, "refund_101", gateway.refundReq.OutRefundNo)
		})
	}
}

func TestRefundSweepStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &refundSweepRepo{}
	uc := NewPaymentUsecase(nil, repo, nil, nil, nil, nil, nil, log.DefaultLogger)
	_, err := uc.ReconcilePendingRefunds(ctx, time.Minute, 100)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, repo.cursors)
}
