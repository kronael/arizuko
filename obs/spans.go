package obs

// Tracing for the five span types in spec 5/O. StartSpan is a no-op when
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is unset: the TracerProvider is never
// built, so StartSpan returns ctx unchanged and a no-op end func — zero
// overhead. When set, spans export over OTLP http/protobuf and join the
// turn's trace via the SpanContext WithTurn/ExtractRequest stamped on ctx.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer     trace.Tracer
	tracerOnce sync.Once
	tracesOn   bool
)

// SetupTraces builds the OTLP TracerProvider when
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set. Call once per daemon at the top of
// main() (after obs.Setup). Unset endpoint → no exporter, no provider, returns
// a no-op shutdown. Like Setup, export is best-effort.
func SetupTraces(daemon, instance string) func() {
	if os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func() {}
	}
	ctx := context.Background()
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		slog.Error("obs: otlp trace exporter init failed; traces disabled", "err", err)
		return func() {}
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(daemon),
			semconv.ServiceNamespace("arizuko"),
			semconv.ServiceInstanceID(instance),
			semconv.DeploymentEnvironment(instance),
		),
	)
	if err != nil {
		slog.Warn("obs: trace resource merge incomplete", "err", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	tracerOnce.Do(func() {
		tracer = provider.Tracer("arizuko/" + daemon)
		tracesOn = true
	})
	return func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutCtx); err != nil {
			slog.Warn("obs: otlp trace flush on shutdown failed", "err", err)
		}
	}
}

// StartSpan opens a span named after one of the five span types
// (turn/model_call/mcp_tool/container_spawn/cross_daemon). attrs are key/value
// string pairs (e.g. "folder", folder, "turn_id", id). The returned end func
// records duration, maps err → an outcome attribute, sets span status, and
// ends the span. When traces are off it returns ctx unchanged + a no-op end,
// so call sites are unconditional.
//
//	ctx, end := obs.StartSpan(ctx, "model_call", "model", model, "folder", folder)
//	resp, err := client.CreateMessage(ctx, req)
//	end(err)
func StartSpan(ctx context.Context, name string, attrs ...string) (context.Context, func(error)) {
	if !tracesOn {
		return ctx, func(error) {}
	}
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(pairs(attrs)...))
	return ctx, func(err error) {
		span.SetAttributes(attribute.String("outcome", outcomeOf(err)))
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// EndOutcome ends the span on ctx with an explicit outcome string (when the
// caller knows the outcome without an error value — e.g. "timeout"). For the
// common error-driven case use the func returned by StartSpan instead.
func EndOutcome(ctx context.Context, outcome string) {
	if !tracesOn {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("outcome", outcome))
	if outcome == "error" || outcome == "timeout" {
		span.SetStatus(codes.Error, outcome)
	}
	span.End()
}

// outcomeOf maps an error to a spec 5/O outcome value.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}

// pairs converts a flat ["k","v","k","v"] slice into OTel attributes. An odd
// trailing key is dropped.
func pairs(kv []string) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, attribute.String(kv[i], kv[i+1]))
	}
	return out
}
