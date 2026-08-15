---
status: shipped
depends: [I-tool-call-logging, ../8/F-audit-stream]
---

# specs/5/O — Observability

> **Shipped 2026-06-15.** Three pillars in `obs/`: logs (`obs.Setup`,
> slog with OTLP fanout), traces (`obs/spans.go`), metrics
> (`obs/metrics.go`, `obs/middleware.go`, `/metrics` per daemon). All
> opt-in via standard OTel env vars; zero overhead when unset.

## What this solves

Operators need visibility into a multi-daemon system without editing every
emit site. Three pillars, all optional, unset → stderr-only.

## Decisions

- **One env var per pillar, standard OTel names.**
  `OTEL_EXPORTER_OTLP_ENDPOINT` (logs), `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`
  (spans), `METRICS_ENABLED` (Prometheus `/metrics`). Unset → the provider
  is never built, so call sites stay unconditional at zero cost. Default
  protocol `http/protobuf` — **no gRPC dependency**.
- **Export is best-effort; app correctness must not depend on it.** Batch
  processors drop on overflow and errors are swallowed. Operators who
  can't lose records run a sidecar collector with disk buffering. This is
  why `audit_log` stays SQLite-canonical and is **never** OTLP-exported
  (`../8/F-audit-stream.md`).
- **Correlation without a distributed tracer.** `turn_id` is only
  per-instance unique (a SQLite PK), so the TraceID is
  `sha256(instance + "/" + turn_id)[:16]` — deterministic, globally
  unique, and reconstructible from a log line alone. W3C `traceparent`
  rides `context.Context`: routd stamps at turn open (`obs.WithTurn`),
  clients inject (`obs.InjectRequest`), servers extract
  (`obs.ExtractRequest` in `auth/middleware.go`).
- **Trust boundary on inbound traceparent.** Channel adapters and
  webhooks **ignore** any inbound `traceparent` — an external caller must
  not be able to join or poison an internal trace. routd stamps its own
  once `turn_id` exists.
- **No in-band traceparent over the MCP socket.** routd already knows the
  active `turn_id` when it lifts in-container tool records, so correlation
  rebuilds at the lift site. Adding `_meta.traceparent` to JSON-RPC is
  possible (MCP permits it) but buys nothing today.
- **Bounded label cardinality.** `folder`, `model`, allowlist-bounded
  `host`, normalized route `path`, status class — never an unbounded id.
  A new metric that takes a raw id as a label is a review-blocker.
- **slog stays primary.** OTLP is a fanout, not a replacement; stderr →
  journald remains the ground truth an operator reads.

Non-goals: SIEM webhooks, file rotation, JSONL dumps, custom trace UIs.

## Surface (code is the reference — do not restate here)

- `obs/obs.go` — `Setup`, `WithTurn`, fanout handler. One line per daemon
  at the top of `main()`: `defer obs.Setup("routd", instance)()`.
- `obs/spans.go` — `SetupTraces`, `StartSpan`. `StartSpan` returns the end
  function, so there is no exported `EndOutcome`; `outcomeOf` maps the
  error to the outcome attribute inside it. Four span
  names are defined (`turn`, `mcp_tool`, `container_spawn`,
  `cross_daemon`); `outcome` ∈ `success|error|timeout|canceled`. There is
  no `model_call` span: the model call happens inside the agent container,
  not in a Go daemon, so a Go span site cannot exist (dead name deleted
  2026-08-15). The `arizuko_model_call_duration_seconds` metric had no
  recorder for the same reason and is deleted too (BUGS `L12`).
- `obs/metrics.go` — the metric registry. Twelve families as of 2026-08-15
  (turn, model tokens, container, HTTP, breaker, token). Every family has a
  recorder; the three that never had one — model-call latency and the egress
  pair, whose producer (the egress credential proxy, specs/6/17) is spec-only —
  are deleted rather than advertised empty (BUGS `L12`).
- `obs/middleware.go` — `HTTPMiddleware(daemon)`: request metrics + the
  `cross_daemon` span, mounted before auth so `/metrics` stays public.
- `obs/propagation.go` — `InjectRequest` / `ExtractRequest`.
- Span call sites: `routd/dispatch.go` (`turn`), `ipc/ipc.go` (`mcp_tool`),
  `runed/docker.go` (`container_spawn`), `obs/middleware.go`
  (`cross_daemon`).

## Cross-references

- [`I-tool-call-logging.md`](I-tool-call-logging.md) — the slog field
  schema these records carry.
- [`../8/F-audit-stream.md`](I-tool-call-logging.md) — `audit_log`, the
  transactional partner that is NOT exported.
- [`obs/README.md`](../../obs/README.md) — implementation reference.
