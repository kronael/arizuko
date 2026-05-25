package obs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// turnInstance is the package-level instance string, set by Setup so
// WithTurn can fold instance into the trace ID without a per-call arg.
// Daemons that never call Setup get the empty string and still get
// trace correlation within their own process.
var turnInstance string

// HashTurnID returns a deterministic OTel TraceID from instance + "/" + turnID.
// Same input → same TraceID across daemons in the same instance, so the
// collector groups all events of one turn under one trace.
func HashTurnID(instance, turnID string) trace.TraceID {
	sum := sha256.Sum256([]byte(instance + "/" + turnID))
	var id trace.TraceID
	copy(id[:], sum[:16])
	return id
}

// WithTurn stamps an OTel SpanContext on ctx whose TraceID is derived from
// the turn ID (deterministic) and SpanID is freshly random per turn. Outbound
// HTTP clients reading this ctx propagate the right trace via traceparent.
// Called once per turn at gateway turn-open.
func WithTurn(ctx context.Context, turnID string) context.Context {
	traceID := HashTurnID(turnInstance, turnID)
	var spanID trace.SpanID
	_, _ = rand.Read(spanID[:])
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     false,
	})
	return trace.ContextWithSpanContext(ctx, sc)
}

// SetInstance configures the package-level instance string used by WithTurn.
// Setup calls this; tests that exercise WithTurn directly may call it too.
func SetInstance(instance string) { turnInstance = instance }

// InjectTraceparent writes a W3C traceparent header from ctx's SpanContext.
// No SpanContext on ctx → no header written.
func InjectTraceparent(ctx context.Context, h http.Header) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
}

// ExtractTraceparent reads a W3C traceparent header into a new ctx. Used by
// non-trust-boundary daemons receiving HTTP from sibling daemons. At trust
// boundaries (channel-adapter ingress, webhook) callers ignore the result
// and stamp their own via WithTurn.
func ExtractTraceparent(ctx context.Context, h http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(h))
}

func init() {
	// Register the W3C TraceContext propagator as the default.
	otel.SetTextMapPropagator(propagation.TraceContext{})
}
