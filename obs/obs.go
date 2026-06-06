// Package obs wires OTLP export over the slog stream. Spec: 5/O.
//
// Setup is called once per daemon in main(). When OTEL_EXPORTER_OTLP_ENDPOINT
// is unset, slog gets a stock JSON handler and Setup returns a no-op
// shutdown. When set, slog records fan out to stderr AND an OTLP log
// exporter. audit_log stays SQLite-canonical; OTLP is observability only.
package obs

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Setup installs slog.Default for the daemon. If OTEL_EXPORTER_OTLP_ENDPOINT
// is unset, installs the stock JSON handler and returns a no-op shutdown.
// Otherwise installs a fanout handler (stderr + OTLP bridge) and returns a
// shutdown func that flushes the exporter.
//
// Typical use:
//
//	defer obs.Setup("gated", instance)()
func Setup(daemon, instance string) func() {
	stock := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: levelFromEnv(),
	})

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		slog.SetDefault(slog.New(stock))
		return func() {}
	}

	ctx := context.Background()
	exporter, err := otlploghttp.New(ctx)
	if err != nil {
		// Exporter init failed; fall back to stock and log once.
		slog.SetDefault(slog.New(stock))
		slog.Error("obs: otlp exporter init failed; OTLP disabled", "err", err)
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
		slog.Warn("obs: resource merge incomplete", "err", err)
	}

	provider := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(exporter)),
		log.WithResource(res),
	)

	bridge := otelslog.NewHandler(daemon, otelslog.WithLoggerProvider(provider))
	slog.SetDefault(slog.New(&fanout{stderr: stock, otel: bridge}))

	return func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutCtx); err != nil {
			slog.Warn("obs: otlp flush on shutdown failed", "err", err)
		}
	}
}

// levelFromEnv reads LOG_LEVEL (debug|info|warn|error, case-insensitive),
// default info. Applies to the stderr stream whether or not OTLP is enabled,
// so a daemon's verbosity is one env var, not a recompile.
func levelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
