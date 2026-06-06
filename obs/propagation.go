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
// (instance, turnID) — deterministic, so every daemon that handles the turn
// shares one trace — and whose SpanID is fresh per call. Call it ONCE at the
// origin (gateway turn-open); downstream daemons inherit the trace via
// ExtractRequest, never a second WithTurn. Only logs emitted through the
// *Context slog APIs (slog.InfoContext, …) on this ctx carry the TraceID to
// the collector — plain slog.Info has no ctx and stays uncorrelated.
func WithTurn(ctx context.Context, instance, turnID string) context.Context {
	traceID := HashTurnID(instance, turnID)
	var spanID trace.SpanID
	if _, err := rand.Read(spanID[:]); err != nil {
		// A zero SpanID makes the SpanContext invalid and silently suppresses
		// propagation; on the (rare) rand failure derive a stable non-zero one.
		copy(spanID[:], traceID[8:])
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(ctx, sc)
}

// InjectRequest writes a W3C traceparent header onto an outbound request from
// ctx's SpanContext, so a sibling daemon can join the same trace. No
// SpanContext on ctx → no header (harmless). Call right before sending any
// cross-daemon HTTP request.
func InjectRequest(ctx context.Context, req *http.Request) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}

// ExtractRequest returns the request's context augmented with any inbound
// traceparent — use it as the base ctx for the handler's work and *Context
// logging. At trust boundaries (channel-adapter ingress, webhook) ignore this
// and mint a fresh trace with WithTurn instead.
func ExtractRequest(req *http.Request) context.Context {
	return otel.GetTextMapPropagator().Extract(req.Context(), propagation.HeaderCarrier(req.Header))
}

func init() {
	// W3C TraceContext is the default propagator process-wide. Cheap, one-time,
	// and harmless when OTLP is disabled (nothing reads it without WithTurn).
	otel.SetTextMapPropagator(propagation.TraceContext{})
}
