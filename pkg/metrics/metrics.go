package metrics

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitMeter wires the OTLP/gRPC metric exporter. Endpoint configuration is
// delegated to the OpenTelemetry SDK's standard environment parsing: an
// OTEL_EXPORTER_OTLP_ENDPOINT (or signal-specific variant) with an https
// scheme exports over TLS, http/unix stay plaintext, and
// OTEL_EXPORTER_OTLP_CERTIFICATE / OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE
// select a trusted CA. Only when no endpoint is configured at all do we fall
// back to the unauthenticated plaintext collector of the local compose stack.
func InitMeter(serviceName string) func() {
	var opts []otlpmetricgrpc.Option
	if !otlpEndpointConfigured("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") {
		opts = append(opts,
			otlpmetricgrpc.WithEndpoint("otel-collector:4317"),
			otlpmetricgrpc.WithInsecure(),
		)
	}
	ctx := context.Background()
	exporter, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		panic(err)
	}
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName))),
	)
	otel.SetMeterProvider(provider)
	return func() {
		if err := provider.Shutdown(ctx); err != nil {
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
