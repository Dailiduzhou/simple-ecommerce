package metrics_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/internal/testutil"
	appmetrics "github.com/Dailiduzhou/simple-ecommerce/pkg/metrics"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otlpmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
)

// metricsCollector is a minimal OTLP/gRPC metrics receiver: a plaintext dial
// against the TLS-only listener fails the handshake and records nothing.
type metricsCollector struct {
	otlpmetrics.UnimplementedMetricsServiceServer
	exports chan int
}

func (c *metricsCollector) Export(context.Context, *otlpmetrics.ExportMetricsServiceRequest) (*otlpmetrics.ExportMetricsServiceResponse, error) {
	c.exports <- 1
	return &otlpmetrics.ExportMetricsServiceResponse{}, nil
}

func serveOTLPMetrics(t *testing.T, tlsEnabled bool) (addr, caFile string, exports <-chan int) {
	t.Helper()
	collector := &metricsCollector{exports: make(chan int, 16)}
	addr, caFile = testutil.ServeGRPC(t, tlsEnabled, func(server *grpc.Server) {
		otlpmetrics.RegisterMetricsServiceServer(server, collector)
	})
	return addr, caFile, collector.exports
}

func emitOneMeasurement(t *testing.T, shutdown func()) {
	t.Helper()
	counter, err := otel.Meter("otlp-export-test").Int64Counter("otlp.export.test")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)
	shutdown()
}

// An https endpoint must export over TLS with the configured trust CA; the
// previous forced WithInsecure dial would fail the handshake here and no
// export would ever be recorded.
func TestInitMeterExportsOverTLSForHTTPSCollector(t *testing.T) {
	addr, caFile, exports := serveOTLPMetrics(t, true)

	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://"+addr)
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", caFile)
	emitOneMeasurement(t, appmetrics.InitMeter("tls-test"))

	select {
	case <-exports:
	case <-time.After(5 * time.Second):
		t.Fatal("no export reached the TLS collector; the exporter dialed plaintext or rejected the CA")
	}
}

// http endpoints must keep working without TLS (the compose collector case).
func TestInitMeterKeepsHTTPCollectorCompat(t *testing.T) {
	addr, _, exports := serveOTLPMetrics(t, false)

	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)
	emitOneMeasurement(t, appmetrics.InitMeter("http-test"))

	select {
	case <-exports:
	case <-time.After(5 * time.Second):
		t.Fatal("no export reached the plaintext collector")
	}
}

func TestLegacyAndSignalSpecificEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		hostname, signal, tls bool
	}{
		{name: "bare-ip"}, {name: "bare-hostname", hostname: true},
		{name: "signal-bare", signal: true}, {name: "signal-https", signal: true, tls: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, caFile, exports := serveOTLPMetrics(t, tc.tls)
			if tc.hostname {
				_, port, err := net.SplitHostPort(addr)
				require.NoError(t, err)
				addr = net.JoinHostPort("localhost", port)
			}
			endpoint := addr
			if tc.tls {
				endpoint = "https://" + addr
			}
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "")
			t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", "")
			t.Setenv("OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE", caFile)
			t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000")
			if tc.signal {
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "invalid-ignored-general-endpoint")
				t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", endpoint)
			}
			emitOneMeasurement(t, appmetrics.InitMeter("endpoint-test"))
			select {
			case <-exports:
			case <-time.After(time.Second):
				t.Fatal("no export reached configured collector")
			}
		})
	}
}
