---
status: partial
depends: [17-openapi-mcp]
---

# specs/5/I — per-tool-call logging

> **Status (2026-08-06).** Partial. `runed` now owns an `audit_log`
> (migration `0005`) and emits on the run-slot calls — `run.hold` (POST
> `/v1/holds`) and `run.kill` (DELETE `/v1/runs/{id}`, POST `/v1/runs/stop`) —
> each naming the caller. Per-turn dispatch deliberately writes NO row: the
> `spawns` row is already that record (see `runed/audit.go`; PLAN.md § SKIP).
> Still open before this ships: **denied** runed calls are unrecorded (an
> `authz.deny` gate, uniform across every endpoint — not a kills-only slice);
> no operator-facing page under `template/web/pub/` describes the audit trail;
> `dashd` renders `spawns` but no `audit_log` view, so the rows are
> `sqlite3`-only; and Open question 1 (redaction regex, 1 KB-cap encoding)
> is still unpinned. BUGS `F7`.

> **Shipped 2026-06-14.** Layer A: `audit_log` (params_summary,
> duration_ms, turn_id, surface, redaction) emitted in-tx via
> `audit.EmitInTx` across every daemon. Layer B: PreToolUse/PostToolUse
> hooks in `ant/src/tool-log.ts`, wired via `ant/src/claude.ts`,
> captured by `container/runner.go` which forwards `[ant] [tool]` lines to
> slog.

## What this solves

Both the platform surface (every MCP tool and REST endpoint) and the agent
sandbox (Bash, Edit, Read, Write, Task, …) produce a torrent of calls per
turn, and before this spec there was no uniform record: agent-internal
tool use was invisible to the operator and platform-side reads weren't
logged at all. No single place answered "what did agent X do in turn Y",
no replay substrate, no acceptance signal for `8/1`'s "exactly one audit
row per state transition".

## Two layers, one shape

- **Layer A — platform-side.** Every MCP tool invocation and every REST
  hit emits one slog line. State-changing calls additionally write one
  `audit_log` row **in the same DB transaction as the mutation**.
- **Layer B — inside the container.** The agent's own tool use is captured
  by harness `PreToolUse`/`PostToolUse` hooks, surfaced as one
  `[ant] [tool]` JSON line per phase on **stderr**, which runed's
  container-log tap lifts into slog. **No DB write** — agent-internal tool
  use is operational, not platform audit; the platform mutations the agent
  reaches through MCP are already Layer A.

They share one field schema, so `journalctl | grep tool=Bash` and
`journalctl | grep tool=set_routes` return rows of the same shape. That
shared schema is the deliverable of this spec.

## Slog field schema (canonical — both layers)

| Key              | Type   | Notes                                                                                |
| ---------------- | ------ | ------------------------------------------------------------------------------------ |
| `actor`          | string | `telegram:user/123`, `agent:atlas/support`, `cli:operator`, `web:slink:<token-hash>` |
| `actor_sub`      | string | Resolved identity sub (`google:114alice`); empty if unauthenticated                  |
| `tool`           | string | `set_routes` / `Bash` / `Edit` / `/v1/routes` (REST path stand-in)                   |
| `surface`        | string | `mcp` \| `rest` \| `agent_pretool` \| `agent_posttool`                               |
| `resource`       | string | `routes`, `chats`, `secrets`; file path for `Edit`/`Write`                           |
| `params_summary` | json   | Small JSON; sensitive params redacted; truncated to 1 KB                             |
| `outcome`        | string | `ok` \| `error`                                                                      |
| `error`          | string | Present only when `outcome=error`                                                    |
| `duration_ms`    | int    | Wall-clock; omitted for `agent_pretool` (no duration yet)                            |
| `turn_id`        | string | Agent-initiated calls and any handler called inside a turn                           |
| `folder`         | string | Group folder, e.g. `atlas/support`                                                   |
| `instance`       | string | Instance name (`krons`, `marinade`)                                                  |

`actor` + `actor_sub` answer "who"; `tool` + `resource` +
`params_summary` answer "what"; `outcome` + `error` + `duration_ms` answer
"how it went"; `turn_id` + `folder` + `instance` answer "where". It
extends the `caller/resource/action/surface/target/result` shape of
[`5/17` Audit contract](17-openapi-mcp.md#audit-contract) with
`params_summary`, `duration_ms`, `turn_id`, and pins the `surface` enum
for both layers. `audit_log` carries the same columns plus `id` and
`created_at`.

## Decisions

- **The audit row is the source of truth; slog is telemetry.** The row is
  written in the same SQLite transaction as the mutation — **if the audit
  insert fails, the mutation rolls back**. slog is lossy by design
  (journald rotation, level filtering). If the two disagree, the table
  wins. slog exists so interactive ops don't round-trip SQLite per call;
  the row exists so "every ACL write last quarter" is one deterministic
  query.
- **Read/write split.** State-changing operations write an `audit_log`
  row AND a slog line; read-only ones (`list_*`, `get_*`, `inspect_*`, all
  REST GETs) emit slog only — the volume is too high and the row carries
  no decision value. The classifier is a per-resource declaration, not
  magic: `resreg.Endpoint`/`resreg.MCPTool` carry an `Action`, and
  `Action.Mutates()` is the gate. Hand-rolled handlers tag themselves via
  an explicit `audit.WriteRow(...)` call site.
- **Layer B does no IPC and no DB write** — it formats and prints, budget
  <10 ms per tool call. A hook that blocks on the platform would make
  every agent tool call a distributed transaction.
- **`audit_log` is per-daemon.** Each daemon owns and migrates its own DB
  and its own audit table (`5/E`, `5/P`); correlation across them is the
  `turn_id`, not a shared table.
- **A table that is already the record is not audited twice.** Where a
  mutation's own row carries everything an audit row would — and more —
  the audit row is noise at the same volume. That is why `messages` is
  skipped, and why `runed` audits who claimed or freed a folder's run
  slot but writes nothing per turn: `spawns` holds kind, state, outcome,
  exit code and every timestamp, and `dashd` renders it. What such a
  table never holds is **who asked**, so the audit row's content is the
  caller, and the calls worth one are the ones expressing intent.

## Open questions

1. **Secret redaction in `params_summary`** — the exact regex set
   (`password`, `token`, `key`, `secret`, …) and the encoding for fields
   that hit the 1 KB cap are still unpinned.
2. **Self-introspection via MCP** — a `query_audit` tool scoped to the
   agent's own folder. Lean yes: it closes "remember what I did last turn"
   without the agent keeping its own journal.
3. **Retention** — `audit_log` grows monotonically. Defer until an
   instance hits 1 GB.
4. **Per-model-invocation logging** — desirable for cost + replay, but the
   SDK has no clean hook surface. `cost_log` covers it for now.

## Non-goals

- **OTLP export** — [`O-observability.md`](O-observability.md) tees the
  slog stream without moving the source of truth; `audit_log` stays
  SQLite-canonical.
- **Replacing slog with audit_log** — both stay; different rate,
  durability, and query shape.
- **Logging full LLM responses inline** — too large. The per-turn artifact
  under `~/turns/` ([`9/3`](8-yaml-manifests.md)) carries the verbatim
  output; `audit_log` carries the pointer.

## Cross-references

- [`17-openapi-mcp.md`](17-openapi-mcp.md) — the
  `Caller`/`Resource`/`Action` shape this extends and the audit contract.
- [`8-yaml-manifests.md` §OpenAPI emission](8-yaml-manifests.md#openapi-emission)
  — the endpoint catalog logged against.
- `../8/F-audit-stream.md` — defines the
  `audit_log` table this spec gives the field schema.
