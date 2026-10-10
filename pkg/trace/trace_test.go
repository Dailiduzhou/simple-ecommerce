package trace_test

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/internal/testutil"
	apptrace "github.com/Dailiduzhou/simple-ecommerce/pkg/trace"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otlptrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	collector := &traceCollector{exports: make(chan int, 16)}
	server := grpc.NewServer()
	if tlsEnabled {
		var cert tls.Certificate
		cert, caFile = testutil.SelfSignedCertificate(t)
		server = grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})))
	}
	otlptrace.RegisterTraceServiceServer(server, collector)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), caFile, collector.exports
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

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)
	emitOneSpan(t, apptrace.InitTracer("http-test"))

	select {
	case <-exports:
	case <-time.After(5 * time.Second):
		t.Fatal("no export reached the plaintext collector")
	}
}
