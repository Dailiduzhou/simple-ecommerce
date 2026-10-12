package biz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Model the SQL guard: only pending rows can be deferred/rejected. A failure
// recording the error must not look like a successfully persisted deferral.
type refundOutcomeRepo struct {
	*paymentTestRepo
	prepareErr, recordErr, applyErr error
}

func (r *refundOutcomeRepo) PreparePaymentRefund(ctx context.Context, id int64, number string) (*PaymentDO, *PaymentRefund, error) {
	if r.prepareErr != nil {
		return nil, nil, r.prepareErr
	}
	return r.paymentTestRepo.PreparePaymentRefund(ctx, id, number)
}
func (r *refundOutcomeRepo) RecordPaymentRefundError(_ context.Context, _ int64, _ string, definitive bool) error {
	r.refundErrorCalls++
	if r.recordErr != nil {
		return r.recordErr
	}
	if r.refund.Status != PaymentRefundStatusPending {
		return ErrPaymentStateConflict
	}
	if definitive {
		r.refund.Status = PaymentRefundStatusFailed
	}
	return nil
}

func (r *refundOutcomeRepo) ApplyPaymentRefund(ctx context.Context, paymentID, refundID int64) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	return r.paymentTestRepo.ApplyPaymentRefund(ctx, paymentID, refundID)
}

func TestRefundCompensationMetricOutcomes(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(ctx)) })
	collect := func() map[string]int64 {
		var data metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(ctx, &data))
		values := map[string]int64{}
		for _, scope := range data.ScopeMetrics {
			for _, metric := range scope.Metrics {
				if metric.Name != "payment_refund_reconcile_total" {
					continue
				}
				for _, point := range metric.Data.(metricdata.Sum[int64]).DataPoints {
					v, _ := point.Attributes.Value("result")
					values[v.AsString()] += point.Value
				}
			}
		}
		return values
	}
	failure := errors.New("simulated failure")
	for _, tc := range []struct {
		name, outcome, status, initialStatus        string
		result                                      *PaymentRefundResult
		gatewayErr, prepareErr, recordErr, applyErr error
		calls, settled                              int
	}{
		{name: "explicit rejection", outcome: "rejected", status: PaymentRefundStatusFailed, result: &PaymentRefundResult{Rejection: true}, gatewayErr: failure, calls: 1},
		{name: "rejection without error", outcome: "rejected", status: PaymentRefundStatusFailed, result: &PaymentRefundResult{Rejection: true}, calls: 1},
		{name: "transient channel failure", outcome: "deferred", status: PaymentRefundStatusPending, gatewayErr: failure, calls: 1},
		{name: "preparation failure", outcome: "deferred", status: PaymentRefundStatusPending, prepareErr: failure, calls: 1},
		{name: "deferral write failure", outcome: "record_error", status: PaymentRefundStatusPending, gatewayErr: failure, recordErr: failure, calls: 1},
		{name: "rejection write failure", outcome: "record_error", status: PaymentRefundStatusPending, result: &PaymentRefundResult{Rejection: true}, gatewayErr: failure, recordErr: failure, calls: 1},
		{name: "settlement failure", outcome: "deferred", status: PaymentRefundStatusPending, result: &PaymentRefundResult{Success: true}, applyErr: failure, calls: 1},
		{name: "concurrently failed", outcome: "record_error", status: PaymentRefundStatusFailed, initialStatus: PaymentRefundStatusFailed, prepareErr: failure, calls: 1},
		{name: "concurrently settled", outcome: "record_error", status: PaymentRefundStatusSuccess, initialStatus: PaymentRefundStatusSuccess, prepareErr: failure, calls: 1},
		{name: "settled", outcome: "settled", status: PaymentRefundStatusSuccess, result: &PaymentRefundResult{Success: true}, settled: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payment, refund := refundFixture(PaymentRefundStatusPending)
			if tc.initialStatus != "" {
				refund.Status = tc.initialStatus
			}
			repo := &refundOutcomeRepo{paymentTestRepo: &paymentTestRepo{payment: payment, refund: refund, staleRefunds: []PaymentRefund{*refund}}, prepareErr: tc.prepareErr, recordErr: tc.recordErr, applyErr: tc.applyErr}
			gateway := &paymentTestGateway{capabilities: PaymentCapabilities{SupportsRefund: true}, refundResult: tc.result, refundErr: tc.gatewayErr}
			uc := NewPaymentUsecase(gateway, repo, nil, nil, nil, nil, nil, log.DefaultLogger)
			before := collect()
			settled, err := uc.ReconcilePendingRefunds(ctx, time.Minute, 100)
			require.NoError(t, err)
			require.Equal(t, tc.settled, settled)
			require.Equal(t, tc.status, refund.Status)
			require.Equal(t, tc.calls, repo.refundErrorCalls, "failure must be recorded exactly once")
			after := collect()
			for _, label := range []string{"settled", "deferred", "rejected", "record_error"} {
				want := int64(0)
				if label == tc.outcome {
					want = 1
				}
				require.Equal(t, want, after[label]-before[label], label)
			}
		})
	}
}
