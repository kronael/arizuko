---
status: draft
depends: [I-tool-call-logging, ../6/F-audit-stream]
---

# specs/5/O — OTLP export

## What this solves

`journalctl` over slog is one substrate per host; it does not aggregate
across instances or feed an OTel collector. This spec ships the **slog
stream** over OTLP **logs** so operators attach any OTel-compatible
collector without changing emit sites.

`audit_log` stays SQLite-canonical ([`I-tool-call-logging.md`](I-tool-call-logging.md),
[`../6/F-audit-stream.md`](../6/F-audit-stream.md)). OTLP is
observability only — lossy by the same logic that makes slog lossy.

## What gets exported

Every slog event → one OTLP log record. Records carrying `turn_id` get
a deterministic trace ID so the collector groups a turn's events.

NOT exported: `audit_log` rows, message content (PII), OTel metrics or
spans (logs-only — see "Logs, not spans").

## Configuration — one env var

`OTEL_EXPORTER_OTLP_ENDPOINT` (W3C standard). Unset → OTLP disabled,
stock JSON handler, zero overhead. Set → events fan out to stderr AND
OTLP. Standard OTel env vars (`OTEL_EXPORTER_OTLP_PROTOCOL`,
`OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_RESOURCE_ATTRIBUTES`, …) are read
by the SDK. Default protocol `http/protobuf` — no gRPC dep.

Resource attrs set by the library: `service.name=<daemonName>`,
`service.namespace=arizuko`, `service.instance.id=<instance>`,
`deployment.environment=<instance>` (override via standard
`OTEL_RESOURCE_ATTRIBUTES`).

## Library shape — `obs/`

One package, one call per daemon at top of `main()`:

```go
defer obs.Setup("gated", coreCfg.Name)()
```

Endpoint unset → stock JSON handler + no-op shutdown. Set →
`otlploghttp` exporter + `LoggerProvider` + fanout `slog.Handler` that
tees every record to stderr AND the OTel bridge.

Stock handler stays primary. OTLP errors are swallowed; batch processor
drops on overflow (SDK default queue cap). **App correctness MUST NOT
depend on export success.** Operators who can't lose records run a
sidecar collector with disk buffering.

## Correlation

`turn_id` is per-instance unique (SQLite message PK; see
`gateway/gateway.go ## turnID`). To make trace IDs globally unique:
`Record.TraceID = sha256(instance + "/" + turn_id)[:16]`. 128-bit
collision is negligible. Trace IDs are not auth material.

Everything else (`request_id`, `actor`, `actor_sub`, `folder`,
`duration_ms`, `session_id`, `tool_call_id`) rides as OTLP attributes
under the `arizuko.*` namespace. No span IDs — no parent/child model.

W3C `traceparent` propagation uses `context.Context` as carrier:

- Gateway opens a turn → `ctx = obs.WithTurn(ctx, turnID)`. That
  stamps an OTel `SpanContext` on ctx with the deterministic TraceID
  above and a fresh random SpanID.
- Outbound HTTP clients (`chanlib/router_client.go`, etc.) call
  `obs.InjectTraceparent(ctx, req.Header)`. ctx with SpanContext →
  `traceparent` written; no SpanContext → no header.
- At trust boundaries (channel adapter, webhook) inbound `traceparent`
  is **ignored for routing**; gateway stamps its own once `turn_id`
  exists. Raw inbound preserved as `arizuko.upstream_traceparent`.
- Gateway↔container MCP unix socket does NOT carry `traceparent` in
  v1. Gateway already knows the active `turn_id` when it lifts
  in-container tool records ([`I-tool-call-logging.md`](I-tool-call-logging.md)
  Layer B); correlation rebuilds at the lift site. See Open Q1.

## Logs, not spans

OTLP **logs**, not **traces**. slog records are point-in-time; spans
need start/end + parent/child + per-emit instrumentation. `duration_ms`
rides as an attribute. Trace views are collector-side reconstructions
(e.g. Tempo logs→traces) — lossy, not authoritative.

## Open questions

1. **In-band trace context over the MCP unix socket.** JSON-RPC has no
   header slot. If demanded: add `_meta.traceparent` per call (MCP
   permits `_meta`) or stay with gateway-side rebuild.
2. **`otelslog` bridge trace-ID slot.** Contrib bridge may not expose
   `Record.TraceID` directly; fallback is a custom `log.Processor` in
   `obs/`. Decided during implementation.
3. **Selective spans for envelope ops.** Logs-only leaves
   latency-breakdown views weak. A future spec may add real spans for a
   narrow set (turn lifecycle, outbound model call, outbound MCP/tool
   call). Out of scope here — keeps the "one Setup() call" contract.

## Non-goals

- OTLP export of `audit_log`.
- SIEM webhooks, file rotation, JSONL dumps.
- Manual span creation per call.
- Replacing slog — stderr stays primary.
- OTel metrics / tracing SDKs. Logs only.

## Cross-references

- [`I-tool-call-logging.md`](I-tool-call-logging.md) — slog field schema
  exported by this spec.
- [`../6/F-audit-stream.md`](../6/F-audit-stream.md) — audit_log table
  (NOT exported).
- [`../7/1-mcp-rest-unification.md`](../7/1-mcp-rest-unification.md) —
  the unified handler that emits the slog records.
