package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	meter                         = otel.Meter("simple-ecommerce/mall")
	paymentStateTransition        = counter("payment_state_transition_total")
	paymentStateConflict          = counter("payment_state_conflict_total")
	paymentAmountMismatch         = counter("payment_amount_mismatch_total")
	paymentCallback               = counter("payment_callback_total")
	paymentCallbackPersistFailure = counter("payment_callback_persist_failure_total")
	paymentReconcileJob           = counter("payment_reconcile_job_total")
	paymentReconcileRequired      = counter("payment_reconcile_required_total")
	riverJobDiscarded             = counter("river_job_discarded_total")
	cacheOperationFailure         = counter("cache_operation_failure_total")
	authorizationDenied           = counter("authorization_denied_total")
)

func counter(name string) metric.Int64Counter { value, _ := meter.Int64Counter(name); return value }
func add(ctx context.Context, counter metric.Int64Counter, attrs ...attribute.KeyValue) {
	counter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func PaymentTransition(ctx context.Context, from, to, event, provider string) {
	add(ctx, paymentStateTransition, attribute.String("from", from), attribute.String("to", to), attribute.String("event", event), attribute.String("provider", provider))
}
func PaymentConflict(ctx context.Context, provider string) {
	add(ctx, paymentStateConflict, attribute.String("provider", provider))
}
func PaymentAmountMismatch(ctx context.Context, provider string) {
	add(ctx, paymentAmountMismatch, attribute.String("provider", provider))
}
func PaymentCallback(ctx context.Context, provider, result string) {
	add(ctx, paymentCallback, attribute.String("provider", provider), attribute.String("result", result))
}
func PaymentCallbackPersistFailure(ctx context.Context, provider string) {
	add(ctx, paymentCallbackPersistFailure, attribute.String("provider", provider))
}
func PaymentReconcileJob(ctx context.Context, provider, result string) {
	add(ctx, paymentReconcileJob, attribute.String("provider", provider), attribute.String("result", result))
}
func PaymentReconcileRequired(ctx context.Context, provider string) {
	add(ctx, paymentReconcileRequired, attribute.String("provider", provider))
}
func RiverJobDiscarded(ctx context.Context, kind string) {
	add(ctx, riverJobDiscarded, attribute.String("kind", kind))
}
func CacheFailure(ctx context.Context, operation, entity string) {
	add(ctx, cacheOperationFailure, attribute.String("operation", operation), attribute.String("entity", entity))
}
func AuthorizationDenied(ctx context.Context, operation, reason string) {
	add(ctx, authorizationDenied, attribute.String("operation", operation), attribute.String("reason", reason))
}
