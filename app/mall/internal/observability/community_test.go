package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestCommunityEventAndDiscardMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	CommunityEvent(context.Background(), "media_delete", "error")
	RiverJobDiscarded(context.Background(), "delete_media")
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
}
