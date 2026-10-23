package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// All emissions share one provider here: the package-level instruments are
// delegating and bind permanently once a provider is installed, so a second
// test installing another provider would silently collect nothing.
func TestBoundedLabelMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	ctx := context.Background()
	CommunityEvent(ctx, "media_delete", "error")
	RiverJobDiscarded(ctx, "delete_media")
	PaymentRefundReconcile(ctx, "deferred")
	// Two shared loads started, one finished: the in-flight gauge reports one
	// running flight and the result counter counts the completion.
	CacheSharedWorkStarted(ctx, "product")
	CacheSharedWorkStarted(ctx, "product")
	CacheSharedWorkFinished(ctx, "product", "loaded")
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &data))
	observed := map[string]map[string]string{}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, point := range sum.DataPoints {
				require.EqualValues(t, 1, point.Value)
				labels := map[string]string{}
				for _, attr := range point.Attributes.ToSlice() {
					labels[string(attr.Key)] = attr.Value.AsString()
				}
				observed[metric.Name] = labels
			}
		}
	}
	require.Equal(t, map[string]string{"operation": "media_delete", "result": "error"}, observed["community_operation_total"])
	require.Equal(t, map[string]string{"kind": "delete_media"}, observed["river_job_discarded_total"])
	require.Equal(t, map[string]string{"result": "deferred"}, observed["payment_refund_reconcile_total"])
	require.Equal(t, map[string]string{"entity": "product"}, observed["cache_shared_work_inflight"])
	require.Equal(t, map[string]string{"entity": "product", "result": "loaded"}, observed["cache_shared_work_total"])
}
