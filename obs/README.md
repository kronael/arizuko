# obs — slog + OTLP + metrics + spans

Spec: [`../specs/5/O-observability.md`](../specs/5/O-observability.md).

`obs` is the one observability shim every daemon calls. It owns:

1. **Logs**: `slog.Default` → stderr (journald) + optional OTLP logs exporter
2. **Traces**: per-turn spans with W3C traceparent propagation
3. **Metrics**: Prometheus `/metrics` endpoint (planned)

## Setup (operator)

| Env var                              | Pillar  | Effect                                                                          |
| ------------------------------------ | ------- | ------------------------------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT`        | logs    | Unset → stderr JSON only, zero overhead. Set → also ship logs to collector.     |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | traces  | Unset → no spans. Set → export spans to collector.                              |
| `METRICS_ENABLED`                    | metrics | Unset/false → no `/metrics`. true → Prometheus endpoint.                        |
| `LOG_LEVEL`                          | logs    | `debug` \| `info` \| `warn` \| `error` (default `info`). Sets stderr verbosity. |

Standard OTel SDK vars are honoured when endpoints are set:
`OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`,
`OTEL_RESOURCE_ATTRIBUTES`. Each record carries
`service.name=<daemon>`, `service.namespace=arizuko`,
`service.instance.id=<instance>`.

Export is best-effort: batch processors drop on overflow and swallow errors.
`audit_log` stays SQLite-canonical; OTLP is observability only.

## Usage (developer)

One call per daemon at the top of `main()`:

```go
defer obs.Setup("routd", cfg.Name)()   // daemon name, instance name
```

That's the whole requirement. The correlation helpers are wired into the
existing code paths:

```go
// 1. origin (routd turn-open) — once per turn:
ctx := obs.WithTurn(ctx, instance, turnID)   // TraceID = sha256(instance+"/"+turnID)[:16]

// 2. outbound INTERNAL cross-daemon request, before sending:
obs.InjectRequest(ctx, req)                  // writes traceparent; no-op if ctx has no trace

// 3. inbound handler (signed sibling-daemon traffic):
r = r.WithContext(obs.ExtractRequest(r))     // join the caller's trace
```

**Correlation only reaches the collector for logs emitted with the `*Context`
APIs** — `slog.InfoContext(ctx, ...)`, not bare `slog.Info(...)`. Convert any
new turn-scoped log the same way.

At **trust boundaries** (channel-adapter ingress, webhooks, third-party API
clients) do NOT inject or extract — those carry no arizuko trace and must not
receive ours. routd mints the trace with `WithTurn` once a `turn_id` exists.

## Where it's wired

- `obs.Setup` — every daemon `main()`.
- `WithTurn` — `routd/loop.go`, at turn-open.
- `InjectRequest` — `chanlib` RouterClient, `authd` grants, `onbod` reply,
  `runed` run client (internal hops only).
- `ExtractRequest` — `auth/middleware.go` (bearer variants), so every signed
  sibling hop joins the trace.

## Off-path cost

Endpoint unset: no exporter, provider, or batch processor is built. The only
always-on work is a one-time propagator registration, `WithTurn`'s sha256 per
turn, and a header inject/extract per internal request — microseconds, and
arizuko is not latency-sensitive.

## Metrics (planned)

When `METRICS_ENABLED=true`, daemons expose `GET /metrics` (Prometheus format).
Metrics defined in the spec:

- `arizuko_turn_duration_seconds` — turn latency
- `arizuko_model_call_duration_seconds` — Anthropic API latency
- `arizuko_model_tokens_total` — token usage
- `arizuko_container_spawns_total` — spawn attempts
- `arizuko_container_active` — running containers
- `arizuko_requests_total` — HTTP requests per daemon
- `arizuko_circuit_breaker_state` — breaker status
- `arizuko_egress_requests_total` — proxied egress

See spec for full list and label definitions.

## Files

- `obs.go` — `Setup`, fanout handler, level config
- `propagation.go` — `WithTurn`, `InjectRequest`, `ExtractRequest`
- `handler.go` — fanout slog handler
- `metrics.go` — Prometheus registry (planned)
- `spans.go` — span helpers (planned)
