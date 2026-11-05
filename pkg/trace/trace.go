package trace

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/internal/otlpconfig"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracer wires the OTLP/gRPC trace exporter. Endpoint configuration is
// normalized for legacy bare host:port addresses, otherwise delegated to the
// OpenTelemetry SDK's standard environment parsing: an
// OTEL_EXPORTER_OTLP_ENDPOINT (or signal-specific variant) with an https
// scheme exports over TLS, http/unix stay plaintext, and
// OTEL_EXPORTER_OTLP_CERTIFICATE / OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE
// select a trusted CA. Only when no endpoint is configured at all do we fall
// back to the unauthenticated plaintext collector of the local compose stack.
//
// Sampling follows the standard OTEL_TRACES_SAMPLER(_ARG) environment
// variables (e.g. parentbased_traceidratio with a 0..1 probability). With no
// configuration the SDK default (parentbased_always_on) applies; deployments
// that must bound collector volume set the variables in compose/env files
// instead of this code hard-coding a policy.
func InitTracer(serviceName string) func() {
	var opts []otlptracegrpc.Option
	endpoint, err := otlpconfig.EndpointOverride("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if err != nil {
		panic(err)
	}
	if endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpointURL(endpoint))
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
		// No WithSampler here: it would override OTEL_TRACES_SAMPLER(_ARG),
		// which NewTracerProvider reads from the environment first. Unset or
		// invalid variables fall back to the SDK default of
		// parentbased_always_on — i.e. today's full-sampling behaviour.
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
