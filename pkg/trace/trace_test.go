package trace_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/internal/testutil"
	apptrace "github.com/Dailiduzhou/simple-ecommerce/pkg/trace"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otlptrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// traceCollector is a minimal OTLP/gRPC trace receiver. Recording exports
// proves the exporter dialed with the transport the endpoint scheme requires:
// a plaintext dial against the TLS-only listener fails the handshake and
// records nothing.
type traceCollector struct {
	otlptrace.UnimplementedTraceServiceServer
	exports chan int
}

func (c *traceCollector) Export(context.Context, *otlptrace.ExportTraceServiceRequest) (*otlptrace.ExportTraceServiceResponse, error) {
	c.exports <- 1
	return &otlptrace.ExportTraceServiceResponse{}, nil
}

func serveOTLPTraces(t *testing.T, tlsEnabled bool) (addr, caFile string, exports <-chan int) {
	t.Helper()
	collector := &traceCollector{exports: make(chan int, 16)}
	addr, caFile = testutil.ServeGRPC(t, tlsEnabled, func(server *grpc.Server) {
		otlptrace.RegisterTraceServiceServer(server, collector)
	})
	return addr, caFile, collector.exports
}

func emitOneSpan(t *testing.T, shutdown func()) {
	t.Helper()
	_, span := otel.Tracer("otlp-export-test").Start(context.Background(), "exported-span")
	span.End()
	shutdown()
}

// An https endpoint must export over TLS with the configured trust CA; the
// previous forced WithInsecure dial would fail the handshake here and no
// export would ever be recorded.
func TestInitTracerExportsOverTLSForHTTPSCollector(t *testing.T) {
	addr, caFile, exports := serveOTLPTraces(t, true)

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://"+addr)
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", caFile)
	emitOneSpan(t, apptrace.InitTracer("tls-test"))

	select {
	case <-exports:
	case <-time.After(5 * time.Second):
		t.Fatal("no export reached the TLS collector; the exporter dialed plaintext or rejected the CA")
	}
}

// http endpoints must keep working without TLS (the compose collector case).
func TestInitTracerKeepsHTTPCollectorCompat(t *testing.T) {
	addr, _, exports := serveOTLPTraces(t, false)

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)
	emitOneSpan(t, apptrace.InitTracer("http-test"))

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
			addr, caFile, exports := serveOTLPTraces(t, tc.tls)
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
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
			t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", "")
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE", caFile)
			t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000")
			if tc.signal {
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "invalid-ignored-general-endpoint")
				t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", endpoint)
			}
			emitOneSpan(t, apptrace.InitTracer("endpoint-test"))
			select {
			case <-exports:
			case <-time.After(time.Second):
				t.Fatal("no export reached configured collector")
			}
		})
	}
}
