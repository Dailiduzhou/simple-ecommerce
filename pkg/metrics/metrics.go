package metrics

import (
	"context"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/internal/otlpconfig"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitMeter wires the OTLP/gRPC metric exporter. Endpoint configuration is
// normalized for legacy bare host:port addresses, otherwise delegated to the
// OpenTelemetry SDK's standard environment parsing: an
// OTEL_EXPORTER_OTLP_ENDPOINT (or signal-specific variant) with an https
// scheme exports over TLS, http/unix stay plaintext, and
// OTEL_EXPORTER_OTLP_CERTIFICATE / OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE
// select a trusted CA. Only when no endpoint is configured at all do we fall
// back to the unauthenticated plaintext collector of the local compose stack.
func InitMeter(serviceName string) func() {
	var opts []otlpmetricgrpc.Option
	endpoint, err := otlpconfig.EndpointOverride("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	if err != nil {
		panic(err)
	}
	if endpoint != "" {
		opts = append(opts, otlpmetricgrpc.WithEndpointURL(endpoint))
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
