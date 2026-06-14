---
status: partial
depends: [I-tool-call-logging, ../7/F-audit-stream]
---

# specs/5/O — Observability

> **Status (2026-06-14):** Partial. Logs shipped (slog + OTLP export); traces
> and metrics spec'd but not implemented. Missing: `obs/spans.go` (5 span types),
> `obs/metrics.go` (9 metric families), `/metrics` handler. All configured via
> standard OTel env vars; disabled by default. `obs/` package owns setup.

## What this solves

Operators need visibility into a multi-daemon system without modifying code
at every emit site. This spec ships the three observability pillars:

1. **Logs** — slog → stderr (journald) + optional OTLP logs exporter
2. **Traces** — spans for turn lifecycle, model calls, MCP tools, container spawns
3. **Metrics** — Prometheus-style counters/gauges/histograms via `/metrics`

All three are optional. Unset env vars → zero overhead, stderr-only.

## Non-goals

- OTLP export of `audit_log` (SQLite is canonical; see `7/F-audit-stream.md`)
- SIEM webhooks, file rotation, JSONL dumps
- Replacing slog — stderr stays primary
- Custom trace UIs — use any OTel-compatible collector

## Configuration

One env var per pillar:

| Env var                              | Pillar  | Effect                                                       |
| ------------------------------------ | ------- | ------------------------------------------------------------ |
| `OTEL_EXPORTER_OTLP_ENDPOINT`        | logs    | Unset → stderr JSON only. Set → also ship logs to collector. |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | traces  | Unset → no spans. Set → export spans to collector.           |
| `METRICS_ENABLED`                    | metrics | Unset/false → no `/metrics`. true → Prometheus endpoint.     |

Standard OTel SDK vars are honoured when endpoints are set:
`OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`,
`OTEL_RESOURCE_ATTRIBUTES`. Default protocol is `http/protobuf` — no gRPC dep.

Resource attrs set by the library:

- `service.name=<daemon>`
- `service.namespace=arizuko`
- `service.instance.id=<instance>`
- `deployment.environment=<instance>`

Export is best-effort: batch processors drop on overflow, errors are swallowed.
App correctness MUST NOT depend on export success. Operators who can't lose
records run a sidecar collector with disk buffering.

---

## Logs (✓ shipped)

### Library shape

One call per daemon at top of `main()`:

```go
defer obs.Setup("routd", os.Getenv("ARIZUKO_INSTANCE"))()
```

`OTEL_EXPORTER_OTLP_ENDPOINT` unset → stock JSON handler + no-op shutdown.
Set → `otlploghttp` exporter + `LoggerProvider` + fanout handler that tees
every record to stderr AND the OTel bridge.

### Correlation

`turn_id` is per-instance unique (SQLite message PK). To make trace IDs
globally unique: `TraceID = sha256(instance + "/" + turn_id)[:16]`.

W3C `traceparent` propagation uses `context.Context` as carrier:

- **Origin**: routd opens a turn → `ctx = obs.WithTurn(ctx, instance, turnID)`
- **Outbound**: internal HTTP clients call `obs.InjectRequest(ctx, req)`
- **Inbound**: `auth/middleware.go` calls `obs.ExtractRequest(r)` to join trace
- **Trust boundaries**: channel adapters/webhooks ignore inbound `traceparent`;
  routd stamps its own once `turn_id` exists

MCP unix socket: no in-band `traceparent` in v1. routd already knows the active
`turn_id` when it lifts in-container tool records; correlation rebuilds at the
lift site.

### slog field schema

Exported by `I-tool-call-logging.md`. Fields ride as OTLP attributes under
`arizuko.*` namespace: `request_id`, `actor`, `actor_sub`, `folder`,
`duration_ms`, `session_id`, `tool_call_id`.

---

## Traces (✗ not implemented)

### Spans

Spans form a tree per turn. Root span is `turn`; children nest under it.

| Span name         | Start                          | End                        | Attributes                                                    |
| ----------------- | ------------------------------ | -------------------------- | ------------------------------------------------------------- |
| `turn`            | routd claims turn              | reply delivered OR timeout | `folder`, `jid`, `turn_id`, `outcome`                         |
| `model_call`      | routd sends to Anthropic       | response received          | `model`, `folder`, `input_tokens`, `output_tokens`, `cache_*` |
| `mcp_tool`        | agent invokes tool             | result returned            | `tool`, `folder`, `outcome`                                   |
| `container_spawn` | runed starts docker run        | container ready OR failed  | `folder`, `image`, `outcome`                                  |
| `cross_daemon`    | HTTP request to sibling daemon | response received          | `source`, `target`, `method`, `path`, `status`                |

`outcome` values: `success`, `error`, `timeout`, `canceled`.

### Implementation sites

- **`turn`**: `routd/loop.go` — wrap `claimAndDispatch` in span
- **`model_call`**: routd model client — wrap Anthropic API call
- **`mcp_tool`**: `routd/mcp.go` — wrap tool handler execution
- **`container_spawn`**: `runed/docker.go` — wrap docker run lifecycle
- **`cross_daemon`**: `obs/propagation.go` — wrap outbound HTTP with span

### MCP trace context (future)

For full in-container correlation, add `_meta.traceparent` to JSON-RPC:

```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": { "name": "reply", "arguments": {...} },
  "_meta": { "traceparent": "00-<trace_id>-<span_id>-01" }
}
```

MCP permits `_meta` at request level. The container agent reads this and
stamps outbound tool calls. Not v1 — routd-side rebuild suffices for now.

---

## Metrics (✗ not implemented)

Prometheus-style metrics exposed at `GET /metrics` per daemon when
`METRICS_ENABLED=true`.

### Metric definitions

**Turn lifecycle** (routd):

| Metric                          | Type      | Labels              | Description                           |
| ------------------------------- | --------- | ------------------- | ------------------------------------- |
| `arizuko_turn_duration_seconds` | histogram | `folder`, `outcome` | Turn latency from claim to completion |
| `arizuko_turns_total`           | counter   | `folder`, `outcome` | Total turns processed                 |

**Model calls** (routd):

| Metric                                | Type      | Labels                         | Description                                 |
| ------------------------------------- | --------- | ------------------------------ | ------------------------------------------- |
| `arizuko_model_call_duration_seconds` | histogram | `model`, `folder`              | Anthropic API call latency                  |
| `arizuko_model_tokens_total`          | counter   | `model`, `folder`, `direction` | Tokens used (in/out/cache_read/cache_write) |

**Container lifecycle** (runed):

| Metric                               | Type      | Labels              | Description                  |
| ------------------------------------ | --------- | ------------------- | ---------------------------- |
| `arizuko_container_spawns_total`     | counter   | `folder`, `outcome` | Container spawn attempts     |
| `arizuko_container_active`           | gauge     |                     | Currently running containers |
| `arizuko_container_duration_seconds` | histogram | `folder`, `outcome` | Container run time           |

**HTTP surface** (all daemons):

| Metric                             | Type      | Labels                       | Description                  |
| ---------------------------------- | --------- | ---------------------------- | ---------------------------- |
| `arizuko_requests_total`           | counter   | `daemon`, `method`, `status` | HTTP requests by status code |
| `arizuko_request_duration_seconds` | histogram | `daemon`, `method`, `path`   | Request latency              |

**Circuit breaker** (routd):

| Metric                          | Type  | Labels   | Description                   |
| ------------------------------- | ----- | -------- | ----------------------------- |
| `arizuko_circuit_breaker_state` | gauge | `folder` | 0=closed, 1=half-open, 2=open |

**Egress** (crackbox/egred):

| Metric                          | Type    | Labels                     | Description             |
| ------------------------------- | ------- | -------------------------- | ----------------------- |
| `arizuko_egress_requests_total` | counter | `folder`, `host`, `status` | Proxied egress requests |
| `arizuko_egress_bytes_total`    | counter | `folder`, `direction`      | Bytes proxied (in/out)  |

**Auth** (authd):

| Metric                          | Type    | Labels    | Description                                |
| ------------------------------- | ------- | --------- | ------------------------------------------ |
| `arizuko_token_mints_total`     | counter | `type`    | Tokens minted (access/refresh)             |
| `arizuko_token_refreshes_total` | counter | `outcome` | Refresh attempts (success/revoked/expired) |

### Label cardinality

Keep cardinality bounded:

- `folder`: group folder path (bounded by tenant count)
- `model`: model ID (handful of values)
- `host`: egress destination (allowlist-bounded)
- `path`: normalize to route pattern, not full URL
- `status`: HTTP status code (2xx/4xx/5xx buckets acceptable)

### Histogram buckets

Use standard OTel defaults or:

- Turn/container: `[0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600]` seconds
- Model call: `[0.1, 0.25, 0.5, 1, 2, 5, 10, 30]` seconds
- HTTP request: `[0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5]` seconds

---

## Implementation notes

### `obs/` package additions

Current: `Setup`, `WithTurn`, `InjectRequest`, `ExtractRequest`, fanout handler.

Add:

- `obs/metrics.go` — Prometheus registry, metric descriptors, `MetricsHandler()`
- `obs/spans.go` — `StartSpan(ctx, name, attrs)`, `EndSpan(ctx, outcome)`
- `obs/middleware.go` — HTTP middleware for request metrics + spans

### Per-daemon `/metrics` handler

Each daemon mounts `GET /metrics` before auth middleware (public endpoint):

```go
if cfg.MetricsEnabled {
    mux.Handle("GET /metrics", obs.MetricsHandler())
}
```

### Span instrumentation pattern

```go
ctx, end := obs.StartSpan(ctx, "model_call", "model", model, "folder", folder)
resp, err := client.CreateMessage(ctx, req)
end(err) // sets outcome + duration
```

### Migration path

1. Merge spec (this file)
2. Add `obs/metrics.go`, `obs/spans.go`, `obs/middleware.go`
3. Wire `/metrics` in each daemon's main.go
4. Add span instrumentation at listed sites
5. Update daemon READMEs with metrics emitted
6. Document in CLAUDE.md observability section

---

## Cross-references

- [`I-tool-call-logging.md`](I-tool-call-logging.md) — slog field schema
- [`../7/F-audit-stream.md`](../7/F-audit-stream.md) — audit_log (NOT exported)
- [`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) — unified handlers that emit records
- [`obs/README.md`](../../obs/README.md) — implementation reference
