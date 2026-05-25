---
status: draft
depends:
  [5-uniform-mcp-rest, D-mcp-everywhere, V-platform-api, 4-openapi-discoverable]
---

# specs/5/I — per-tool-call logging

## What this solves

Both the platform surface (every MCP tool + every REST endpoint served by
gateway, proxyd, dashd, onbod, webd, davd) and the agent sandbox (Bash,
Edit, Read, Write, Task, ...) produce a torrent of calls per turn. Today
there is no uniform record — `resreg` emits a structured slog line per
dispatch ([`5/D` Q7](D-mcp-everywhere.md)), `cli_audit` covers CLI writes
([`6/F`](../6/F-audit-stream.md)), but agent-internal tool use is invisible
to the operator and platform-side reads aren't logged at all. The result:
no single place to answer "what did agent X do in turn Y", no replay
substrate, no acceptance signal for `7/1`'s "exactly one audit row per
state transition".

This spec defines the field schema and the split between transactional
audit (DB) and operational telemetry (slog → journald). It cuts across
both layers — platform handlers AND the in-container agent — so the
operator sees one shape no matter who initiated the call.

## Two layers, one shape

- **Layer A — platform-side.** Every MCP tool invocation (via `resreg`
  dispatch or hand-rolled `ipc/ipc.go` handler) AND every REST endpoint
  hit (gateway, proxyd, dashd, onbod, webd, davd) emits one slog line.
  State-changing calls additionally write one `audit_log` row in the
  same DB transaction as the resource mutation.
- **Layer B — inside the container.** The agent's own tool use is
  captured via Claude Code SDK `PreToolUse` / `PostToolUse` hooks. The
  hook emits one JSON line per phase to stdout; gateway's container
  log capture picks it up. No DB write — agent-internal tool use is
  operational, not platform audit (the platform-side mutations the agent
  reaches through MCP are already covered by Layer A).

The two layers share the slog field schema below so `journalctl |
grep tool=Bash` and `journalctl | grep tool=set_routes` return rows of
the same shape.

## Slog field schema

Canonical keys, emitted by both layers:

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
| `duration_ms`    | int    | Wall-clock; for `agent_pretool` omit (no duration yet)                               |
| `turn_id`        | string | When applicable (agent-initiated calls and any handler called inside a turn)         |
| `folder`         | string | Group folder, e.g. `atlas/support`                                                   |
| `instance`       | string | Arizuko instance name (`krons`, `marinade`)                                          |

`actor` + `actor_sub` together answer "who". `tool` + `resource` +
`params_summary` answer "what". `outcome` + `error` + `duration_ms`
answer "how it went". `turn_id` + `folder` + `instance` answer "where
in the system". The schema is intentionally close to the
`caller=... resource=... action=... surface=... target=... result=...`
shape already defined in [`5/5` "Audit + observability"](5-uniform-mcp-rest.md)
— this spec extends it with `params_summary`, `duration_ms`, `turn_id`
and pins the surface enum for both layers.

## audit_log shape

Defined in [`6/F`](../6/F-audit-stream.md); this spec is the canonical
field schema. Same columns as the slog keys above, plus:

- `id` INTEGER PRIMARY KEY AUTOINCREMENT
- `created_at` DATETIME NOT NULL DEFAULT (strftime ISO-8601 UTC)

The audit row is the **source of truth** — written in the same SQLite
transaction as the resource mutation. If the audit insert fails, the
mutation rolls back. slog is operational telemetry; lossy by design
(journald rotation, level filtering). If the two disagree, the audit
table wins. The slog stream exists to drive interactive ops and
operator dashboards without round-tripping SQLite per call; the DB
row exists so that "show me every ACL write in the last quarter" is
one query, deterministically.

## Read-vs-write split

- **State-changing** operations (CRUD writes — `create`, `update`,
  `delete`, plus action verbs that mutate: `set_routes`, `add_grant`,
  `schedule_task`, `register_group`, `revoke_token`, ...) write one
  `audit_log` row AND emit one slog line. Same transaction as the
  mutation; rollback on audit failure.
- **Read-only** operations (`list_*`, `get_*`, `inspect_*`, plus all
  REST GETs) emit slog only. No audit row. Volume is too high and
  the row carries no decision value.

The classifier is per-resource declaration: `resreg.Endpoint` and
`resreg.MCPTool` already carry an `Action` ([`5/5` Caller/Resource shape](5-uniform-mcp-rest.md));
`Action.Mutates() bool` is the gate. For hand-rolled handlers outside
the registry, the handler tags itself (`audit.WriteRow(...)` is an
explicit call site, not magic).

## PreToolUse / PostToolUse hook design

Claude Code SDK fires `PreToolUse` before each tool invocation and
`PostToolUse` after, both as configurable hooks
([Claude Code docs — Hooks](https://docs.claude.com/en/docs/claude-code/hooks)).

- **Hook script.** Small bash (or python) wrapper shipped in the
  `arizuko-ant` image at `/usr/local/bin/arizuko-tool-hook`. Reads
  hook JSON on stdin, writes one JSON line per phase to stdout.
- **Configuration.** Wired in the in-tree agent settings
  (`ant/.claude/settings.json` or its template) so every spawned
  container picks it up without per-folder configuration.
- **Output format.** One line per phase, on the container's stdout,
  picked up by gateway's container-log tap:

  ```
  [ant] [tool] {"phase":"pre","tool":"Bash","turn_id":"t-abc","args_summary":"<truncated>","folder":"atlas/support"}
  [ant] [tool] {"phase":"post","tool":"Bash","turn_id":"t-abc","duration_ms":42,"outcome":"ok","folder":"atlas/support"}
  ```

  Same field names as the slog schema; gateway translates the line
  into a slog event when it lifts it out of the container log.

- **Performance budget.** <10 ms overhead per tool call. The hook
  does no IPC and no DB write — it just formats and prints. Layer A
  picks up MCP calls the agent makes back into the platform; those
  carry their own `turn_id` and full audit-row treatment.

## Open questions

1. **Secret redaction in `params_summary`.** State-of-the-art for
   hand-rolled audit: regex-known-keys (`password`, `token`, `key`,
   `secret`, ...) plus a length cap (1 KB). Spec out the exact regex
   set and the encoding for fields that hit the cap.
2. **Self-introspection via MCP.** Should the agent see its own
   `audit_log` rows through a `query_audit` MCP tool, scoped to its
   own folder? Lean yes — it closes the loop on "remember what I did
   last turn" without needing the agent to keep its own journal.
3. **Retention / rotation.** `audit_log` grows monotonically. Size
   cap? Time window? Move to per-month partitioned files after N
   rows? Defer until the first instance hits 1 GB of audit.
4. **LLM API call introspection.** Per-model-invocation logging
   (one row per `messages.create`) is desirable for cost + replay,
   but the SDK has no clean hook surface today. Postponed; track
   via `cost_log` for now.

## Non-goals

- **OTLP export.** Sending audit / telemetry to an external collector
  is a separate spec, postponed. Acknowledged path: the slog stream
  can be tee'd to an OTLP exporter without changing the source of
  truth.
- **Replacing slog with audit_log as the only sink.** slog is the
  high-rate, lossy, interactive channel; audit_log is the durable,
  transactional, queryable channel. Both stay.
- **Logging full LLM responses inline.** Too large; the per-turn
  artifact path under `~/turns/` ([`7/3` "git as truth"](../7/3-git-as-truth.md))
  carries the verbatim turn output. `audit_log` carries the pointer.

## Cross-references

- [`5/5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) — the
  `Caller`/`Resource`/`Action` shape this spec extends; the existing
  audit log-line format that this spec turns into a DB row.
- [`5/D-mcp-everywhere.md`](D-mcp-everywhere.md) — Q7 (audit emit
  contract) is answered here.
- [`5/U-genericization.md`](U-genericization.md) — each daemon owns
  its `audit_log` table or shares the gated one; covered in Q3 of
  this spec when daemon-DB split lands.
- [`5/V-platform-api.md`](V-platform-api.md) — the `/v1/*` surfaces
  whose REST hits this spec covers.
- [`5/4-openapi-discoverable.md`](4-openapi-discoverable.md) — the
  endpoint catalog this spec logs against.
- [`6/F-audit-stream.md`](../6/F-audit-stream.md) — the DB-side
  partner; defines the `audit_log` table that this spec gives the
  field schema.
- [`7/1-mcp-rest-unification.md`](../7/1-mcp-rest-unification.md) —
  the "exactly one audit row per state transition" acceptance
  criterion.
- [`7/3-git-as-truth.md`](../7/3-git-as-truth.md) — turn-end sidecars
  reference `audit_log.id` ranges produced under this spec.
