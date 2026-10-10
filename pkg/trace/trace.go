package trace

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracer wires the OTLP/gRPC trace exporter. Endpoint configuration is
// delegated to the OpenTelemetry SDK's standard environment parsing: an
// OTEL_EXPORTER_OTLP_ENDPOINT (or signal-specific variant) with an https
// scheme exports over TLS, http/unix stay plaintext, and
// OTEL_EXPORTER_OTLP_CERTIFICATE / OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE
// select a trusted CA. Only when no endpoint is configured at all do we fall
// back to the unauthenticated plaintext collector of the local compose stack.
func InitTracer(serviceName string) func() {
	var opts []otlptracegrpc.Option
	if !otlpEndpointConfigured("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") {
		opts = append(opts,
			otlptracegrpc.WithEndpoint("otel-collector:4317"),
			otlptracegrpc.WithInsecure(),
		)
	}

	ctx := context.Background()

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		panic(err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func() {
		if err := tp.Shutdown(ctx); err != nil {
			otel.Handle(err)
		}
	}
}

// otlpEndpointConfigured reports whether any endpoint environment variable is
// present, in which case the SDK's own parsing must win unconditionally:
// overriding the endpoint here would strip the scheme again and force
// plaintext connections onto TLS-only collectors.
func otlpEndpointConfigured(signalSpecificVars ...string) bool {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		return true
	}
	for _, name := range signalSpecificVars {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}
