package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("simple-ecommerce/mall")

// StartSharedCacheWork opens a span for cache-miss work that can outlive the
// request that triggered it. The inherited request span is often already ended,
// so the work gets its own span: child spans and log records attach to a live
// parent while the trace ID is preserved for correlation.
//
// Only bounded, low-cardinality labels are attached; the cache key itself stays
// out of span attributes for the same reason it stays out of metric labels.
func StartSharedCacheWork(ctx context.Context, entity string, budget time.Duration) (context.Context, func()) {
	ctx, span := tracer.Start(ctx, "cache.shared_work",
		trace.WithAttributes(
			attribute.String("cache.entity", entity),
			attribute.Int64("cache.work_budget_ms", budget.Milliseconds()),
		),
	)
	return ctx, func() { span.End() }
}
