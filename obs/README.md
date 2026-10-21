# obs — slog + OTLP + metrics + spans

arizuko records what happens on three substrates. `audit_log` is the record
of what changed and who asked. `slog` is telemetry for everything, written
to stderr and so to journald. OTLP export is an opt-in copy of the logs and
the spans for a collector. `obs` is the shim every daemon calls for the
last two; `audit/README.md` holds the first.

<!-- twin:observability -->

```
┌───────────────────────────────────────────────────────────────────────────┐
│ one event in a daemon: a mutation, a denial, a tool call, a turn          │
└───────────┬───────────────────────────┬───────────────────────┬───────────┘
            ▼                           ▼                       ▼
┌───────────────────────┐ ┌─────────────────────────┐ ┌─────────────────────┐
│ audit_log row         │ │ slog record             │ │ span                │
│ the record            │ │ telemetry, lossy        │ │ opt-in              │
│ WriteFor: rides the   │ │ obs.Setup: JSON on      │ │ obs.StartSpan:      │
│ mutation's            │ │ stderr ► journald;      │ │ turn, mcp_tool,     │
│ GuardedBatch          │ │ an OTLP logs tee when   │ │ container_spawn,    │
│ EmitTo: beside an     │ │ OTEL_EXPORTER_OTLP_     │ │ cross_daemon;       │
│ effect on a platform  │ │ ENDPOINT is set         │ │ exported when       │
│ or a guest            │ │                         │ │ OTEL_EXPORTER_OTLP_ │
│                       │ │                         │ │ TRACES_ENDPOINT set │
└───────────┬───────────┘ └────────────┬────────────┘ └──────────┬──────────┘
            ▼                          └──────────────┬──────────┘
┌───────────────────────┐                             ▼
│ the owner's store:    │ ┌─────────────────────────────────────────────────┐
│ routd, runed, authd,  │ │ collector: TraceID = sha256(instance + "/" +    │
│ onbod; GET /v1/audit  │ │ turn_id)[:16], so one turn is one trace         │
│ through audit.Query   │ │ across daemons                                  │
└───────────┬───────────┘ └─────────────────────────────────────────────────┘
            ▼
┌───────────────────────────────────────────────┐
│ /dash/audit/: asks each owner and merges the  │
│ rows; a source that fails gets a banner       │
└───────────────────────────────────────────────┘
 audit_log is never exported over OTLP. /metrics is a fourth, separate
 door: Prometheus on seven daemons when METRICS_ENABLED is set.
```

<!-- /twin -->

`obs` owns:

1. **Logs**: `slog.Default` → stderr (journald) + optional OTLP logs exporter
2. **Traces**: per-turn spans with W3C traceparent propagation on internal
   hops
3. **Metrics**: the Prometheus `/metrics` endpoint

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
`audit_log` stays the canonical record in the owner databases; OTLP is
observability only.

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

The MCP socket carries no traceparent either, so the guest supplies no
trace. The `mcp_tool` span opens on the socket's own context, not on
the turn's, so it carries the `tool` and `folder` attributes and starts a
trace of its own (`ipc/ipc.go` `serveConn`).

## Where it's wired

- `obs.Setup` — every daemon `main()`, as `defer obs.Setup("name", instance)()`.
- Metrics: keep label cardinality bounded (folder, model, status — never an
  unbounded id).
- Spans: `obs.SetupTraces` in every daemon `main()`; `obs.StartSpan` at four
  sites, so there are four span names:

  | span              | site                                              |
  | ----------------- | ------------------------------------------------- |
  | `turn`            | `routd/dispatch.go`, at turn dispatch             |
  | `mcp_tool`        | `ipc/ipc.go` `serveConn`, one per `tools/call`    |
  | `container_spawn` | `runed/qemu.go` `Run`, one per guest              |
  | `cross_daemon`    | `obs/middleware.go` `HTTPMiddleware`, per request |

  Each span carries `outcome`: `success`, `error`, `timeout` or `canceled`
  (`obs/spans.go` `outcomeOf`). There is no `model_call` span: the model runs
  in the guest, where no Go span site exists. The turn span covers it.

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

## Metrics

Seven daemons mount `GET /metrics` (Prometheus format) behind
`obs.MetricsEnabled()`: authd, dashd, onbod, proxyd, routd, runed, webd. The
collectors exist either way, so the recording helpers stay cheap when the
endpoint is off, and `MetricsHandler` answers 404 rather than nothing. Mount it
BEFORE auth — it is a public endpoint.

Twelve metrics, declared once in `metrics.go`:

| metric                                                                                             | what it counts                                               |
| -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `arizuko_turn_duration_seconds`, `arizuko_turns_total`, `arizuko_silent_turns_total`               | the turn: how long, how many, how many answered nothing      |
| `arizuko_nudged_turns_total`                                                                       | nudged turns completed as `delivered` or `silent`, by folder |
| `arizuko_model_tokens_total`                                                                       | token usage                                                  |
| `arizuko_container_spawns_total`, `arizuko_container_active`, `arizuko_container_duration_seconds` | the spawn                                                    |
| `arizuko_requests_total`, `arizuko_request_duration_seconds`                                       | HTTP, per daemon — every daemon emits these two              |
| `arizuko_circuit_breaker_state`                                                                    | breaker status                                               |
| `arizuko_token_mints_total`, `arizuko_token_refreshes_total`                                       | authd's issuance                                             |

## Files

- `obs.go` — `Setup`, the stderr + OTLP fan-out handler, level config
- `propagation.go` — `WithTurn`, `InjectRequest`, `ExtractRequest`
- `middleware.go` — `HTTPMiddleware`, the `cross_daemon` span and the request
  metrics
- `metrics.go` — the registry, the twelve collectors, `MetricsHandler`
- `spans.go` — `SetupTraces`, `StartSpan`
