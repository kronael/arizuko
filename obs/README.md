# obs — slog → stderr + OTLP, with per-turn trace correlation

Spec: [`../specs/5/O-otlp-export.md`](../specs/5/O-otlp-export.md).

`obs` is the one observability shim every daemon calls. It owns the process's
`slog.Default`: always stderr (journald), and — when an OTLP endpoint is set —
additionally an OTLP **logs** exporter. It also stamps a per-turn trace so one
turn's events line up across daemons in the collector.

## Setup (operator)

Two env vars, both optional:

| Env var                       | Effect                                                                                                                                        |
| ----------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Unset → stderr JSON only, **no exporter built, zero export overhead**. Set → also ship logs to that collector (`http/protobuf`, no gRPC dep). |
| `LOG_LEVEL`                   | `debug` \| `info` \| `warn` \| `error` (default `info`). Sets stderr verbosity whether or not OTLP is on.                                     |

Standard OTel SDK vars are honoured when the endpoint is set:
`OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS`,
`OTEL_RESOURCE_ATTRIBUTES`, … Each record carries
`service.name=<daemon>`, `service.namespace=arizuko`,
`service.instance.id=<instance>`.

Export is best-effort: the batch processor drops on overflow and swallows
errors — stderr stays primary, app correctness never depends on it. `audit_log`
stays SQLite-canonical; OTLP is observability only.

## Usage (developer)

One call per daemon at the top of `main()`:

```go
defer obs.Setup("gated", cfg.Name)()   // daemon name, instance name
```

That's the whole requirement. The three correlation helpers are already wired
(see below); you only touch them when adding a new turn origin or a new
cross-daemon HTTP path:

```go
// 1. origin (gateway turn-open) — once per turn:
ctx := obs.WithTurn(ctx, instance, turnID)   // TraceID = sha256(instance+"/"+turnID)[:16]

// 2. outbound INTERNAL cross-daemon request, before sending:
obs.InjectRequest(ctx, req)                  // writes traceparent; no-op if ctx has no trace

// 3. inbound handler (signed sibling-daemon traffic):
r = r.WithContext(obs.ExtractRequest(r))     // join the caller's trace
```

**Correlation only reaches the collector for logs emitted with the `*Context`
APIs** — `slog.InfoContext(ctx, …)`, not bare `slog.Info(…)`. The gateway's
turn-lifecycle logs use `*Context`; convert any new turn-scoped log the same
way if you want its TraceID.

At **trust boundaries** (channel-adapter ingress, webhooks, third-party API
clients) do NOT inject or extract — those carry no arizuko trace and must not
receive ours. The gateway mints the trace with `WithTurn` once a `turn_id`
exists.

## Where it's wired

- `obs.Setup` — every daemon `main()`.
- `WithTurn` — `gateway` `pollOnce`, at turn-open.
- `InjectRequest` — `chanlib` RouterClient, `authd` grants, `onbod` reply,
  `runed` run client (internal hops only).
- `ExtractRequest` — `auth/middleware.go` (`RequireSigned`/`StripUnsigned` and
  the bearer variants), so every signed sibling hop joins the trace.

## Off-path cost

Endpoint unset: no exporter, provider, or batch processor is built. The only
always-on work is a one-time propagator registration, `WithTurn`'s sha256 per
turn, and a header inject/extract per internal request — microseconds, and
arizuko is not latency-sensitive. The injected `traceparent` is harmless when
nothing consumes it.

## Non-goals

- OTLP export of `audit_log` (SQLite is canonical;
  [`../specs/7/F-audit-stream.md`](../specs/7/F-audit-stream.md)).
- OTel metrics / tracing SDKs — logs only.
- Manual span creation per call (open Q3 in the spec: selective spans later).
- In-band trace context over the gateway↔container MCP socket (open Q1).
