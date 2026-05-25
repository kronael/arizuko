package obs

import (
	"context"
	"log/slog"
)

// fanout is a slog.Handler that tees every record to stderr (primary) and
// the OTel log bridge (additive). OTel errors are swallowed so observability
// never breaks an emit.
type fanout struct {
	stderr slog.Handler
	otel   slog.Handler
}

func (h *fanout) Enabled(ctx context.Context, l slog.Level) bool {
	return h.stderr.Enabled(ctx, l) || h.otel.Enabled(ctx, l)
}

func (h *fanout) Handle(ctx context.Context, r slog.Record) error {
	// stderr is primary; OTel is additive. Swallow OTel errors.
	_ = h.otel.Handle(ctx, r.Clone())
	return h.stderr.Handle(ctx, r)
}

func (h *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanout{stderr: h.stderr.WithAttrs(attrs), otel: h.otel.WithAttrs(attrs)}
}

func (h *fanout) WithGroup(name string) slog.Handler {
	return &fanout{stderr: h.stderr.WithGroup(name), otel: h.otel.WithGroup(name)}
}
