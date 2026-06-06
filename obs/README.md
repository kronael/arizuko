# obs — OTLP export over slog

Spec: [`../specs/5/O-otlp-export.md`](../specs/5/O-otlp-export.md).

## Usage

One call per daemon at top of `main()`:

```go
defer obs.Setup("gated", coreCfg.Name)()
```

- `OTEL_EXPORTER_OTLP_ENDPOINT` unset → stock JSON handler on stderr,
  no-op shutdown, zero overhead.
- Set → events fan out to stderr AND OTLP (`http/protobuf` by default;
  no gRPC dep).

Standard OTel env vars (`OTEL_EXPORTER_OTLP_PROTOCOL`,
`OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_RESOURCE_ATTRIBUTES`, ...) are
read by the SDK.

## Correlation across daemons

Gateway opens a turn:

```go
ctx = obs.WithTurn(ctx, turnID)
```

Outbound HTTP clients:

```go
req, _ := http.NewRequestWithContext(ctx, "POST", url, body)
obs.InjectTraceparent(ctx, req.Header)
```

Receivers (non-trust-boundary daemons):

```go
ctx := obs.ExtractTraceparent(r.Context(), r.Header)
```

`TraceID = sha256(instance + "/" + turn_id)[:16]` so all daemons in
the same instance see the same trace ID for one turn.

At trust boundaries (channel-adapter ingress, webhook handler) the
inbound `traceparent` is **ignored for routing**; the daemon stamps
its own via `WithTurn` once `turn_id` exists. Preserve the inbound
raw value as the `arizuko.upstream_traceparent` slog attribute for
forensics.

## Non-goals

- OTLP export of `audit_log` (SQLite is canonical;
  [`../specs/7/F-audit-stream.md`](../specs/7/F-audit-stream.md)).
- OTel metrics / tracing SDKs (logs only).
- Manual span creation per call.
