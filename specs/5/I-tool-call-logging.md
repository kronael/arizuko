---
status: defected
depends: [17-openapi-mcp]
defects: [F35, F70]
---

# specs/5/I — per-tool-call logging

Write path shipped 2026-06-14 (`audit.EmitInTx` in every mutation's own
transaction, plus the container-side hooks). Read path shipped 2026-08-06:
each owning daemon serves its own table at `GET /v1/audit` and `/dash/audit/`
federates them.

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

| Key              | Type   | Notes                                                                                   |
| ---------------- | ------ | --------------------------------------------------------------------------------------- |
| `actor`          | string | `telegram:user/123`, `agent:atlas/support`, `cli:operator`, `web:slink:<token-hash>`    |
| `actor_sub`      | string | Resolved identity sub (`google:114alice`); empty if unauthenticated                     |
| `tool`           | string | `set_routes` / `Bash` / `Edit` / `/v1/routes` (REST path stand-in)                      |
| `surface`        | string | `mcp` \| `rest` \| `agent_pretool` \| `agent_posttool`                                  |
| `resource`       | string | `routes`, `chats`, `secrets`; file path for `Edit`/`Write`                              |
| `params_summary` | json   | Small JSON; sensitive params redacted; values >200 runes truncated, map capped at 512 B |
| `outcome`        | string | `ok` \| `error`                                                                         |
| `error`          | string | Present only when `outcome=error`                                                       |
| `duration_ms`    | int    | Wall-clock; omitted for `agent_pretool` (no duration yet)                               |
| `turn_id`        | string | Agent-initiated calls and any handler called inside a turn                              |
| `folder`         | string | Group folder, e.g. `atlas/support`                                                      |
| `instance`       | string | Instance name (`krons`, `marinade`)                                                     |

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

## Reading it back

Each owning daemon serves ITS OWN table at `GET /v1/audit`, registered once
as a read-only `resreg` resource (`resreg/resources/audit.go`) and mounted by
`routd`, `runed` and `authd`. `audit.Query` is the single reader; `audit.Row`
is both its scan target and the resreg `RowType`, so the JSON a caller
receives is the struct `/openapi.json` documents.

**Three daemons share one resource name, and that does not violate "name IS
wire identity".** That rule forbids two DIFFERENT tables sharing a wire name.
Here there is one table shape, replicated per owner DB by the per-daemon
decision above, so `/v1/audit` means "this daemon's log" on whichever daemon
answers — which is what lets `dashd` federate by fanning ONE path out rather
than writing three clients.

**No write face, and no `/{id}`.** A create/update/delete on an append-only
evidence table would let a caller forge or erase the record of an act; `id`
is a per-DB AUTOINCREMENT, so a path key would name a different row on each
daemon. The catalog decl also carries no `DB` subsystem, so `arizuko
export`/`apply` never round-trip it.

**Cold tier, read-only.** `audit` is a cold-tier resource in the `5/16` sense
— an operator/agent-managed entity with both faces — not a hot-tier agent
action. Its write half is not an API at all: rows are emitted by the
mutations they record, so the resource publishes exactly the half it can
serve honestly.

**A successful read writes no row.** `Action.Mutates()` is false for `list`,
so `resreg.emitAudit` returns before the insert. Without that, one operator
page-load would append to the table it is reading, forever. Denials and
errors still land — that is the forensic value a read surface has.

### Authorization

Two injected gates, per `5/17`'s rule that `resreg` owns no auth policy:

- **REST**, on all three daemons: the `audit:read` scope. No human bearer can
  hold it — a user token's scope list carries FOLDER GLOBS
  (`routd.handleUserScopes` → `oauth.issueSession`), and `auth.scopeMatches`
  rejects any held value without a colon, so `acme/**` fails and so does an
  operator's own `**`. `service:dashd` is the sole holder and `/dash/audit/`
  is `requireOperator`-gated, so operator-only is a mechanism rather than a
  convention.
- **The agent socket** (routd only): `mcp:query_audit` at the agent's own
  folder, default-deny, with the row filter pinned to that folder. This is
  where a genuinely non-operator caller exists, so it is where folder
  containment does the work.

**Nothing keys authority on the folder claim.** An absent `arz/folder` is not
evidence of operator status: routd stamps a folder only when the sub holds
exactly one scope, so a tenant with two grants arrives equally claimless.
Keying a list-all on that claim is the recorded cross-tenant REST leak. The
claim only ever NARROWS a call the gate already authorized.

Folder bounds are SUBTREE (`acme` reaches `acme/support`), matching how a
grant is written and how `runed.ownsFolder` contains a run. A folder-bounded
read excludes the folder-less rows (`daemon.start`): those are instance-scope
facts, and surfacing them to a tenant because the column is NULL is the same
leak in the other direction.

### The federated page

`/dash/audit/` (`dashd/audit_page.go`) fans out to each owner's `/v1/audit`
and merges on `created_at`, with a Source column. It reaches them over HTTP
and never opens `runed.db` or `auth.db`: dashd is FS-mounted on `routd.db`
alone, and a second reader of a table whose owner publishes no contract is a
recorded defect class.

The cursor is composite (`routd:123,runed:45,authd:7`) because `id` is
per-DB; one integer cannot page three sequences, and paging on `created_at`
would drop rows sharing a millisecond across sources. A source that
contributed no row to a page keeps its cursor rather than resetting, or it
would replay its newest rows on every click. A source that fails gets a
banner naming it — silence would report "nothing happened in runed" when the
truth is "runed did not answer".

## Decisions (read path)

- **Redaction set, pinned** (`audit.redactRE`): `pass(word|phrase)?`,
  `token`, `secret`, `credential`, `authorization`, `cookie`, `dsn`,
  `api_?key`, `^key$`, `[_-]key$`. The `key` alternatives are SINGULAR and
  end-anchored so `serving_keys` — a count in authd's `daemon.start` row —
  stays readable; an unanchored `key` would have redacted it along with
  `monkey`. `dsn` is there because authd wrote its `auth.db` path into
  `params_summary` in the clear, found while auditing the column before
  publishing it. `session` is deliberately absent: `session_id` is a turn
  identifier, not a credential, and redacting it would blind the join this
  table exists for.
- **Cap encoding, pinned**: values over 200 runes are truncated to
  `…<truncated:Nchars>` BEFORE the 512-byte whole-map cap. Previously one fat
  argument pushed the map over and collapsed it to `{"_truncated":true}`,
  taking the caller's folder and the target resource with it — an audit row
  that cannot say what it was about is not a smaller row, it is none.
  Truncation is rune-wise; a byte-wise cut of UTF-8 yields U+FFFD in the one
  field a reader most wants verbatim. Redaction runs first, so a long secret
  is redacted rather than truncated to its exploitable prefix.
- **The cap is 512 bytes**, not the 1 KB earlier drafts of this spec claimed.
  `audit/PLAN.md`, the column comment and the shipped code all said 512 and
  nothing ever implemented 1 KB.

## Open questions

1. **Retention** — `audit_log` grows monotonically. Defer until an
   instance hits 1 GB.
2. **Per-model-invocation logging** — desirable for cost + replay, but the
   SDK has no clean hook surface. `cost_log` covers it for now.
3. **onbod's table is still unreachable** — `onbod` owns an `audit_log`
   (migration `0002`) and writes real rows, but mounts no `/v1/audit`, so
   `dashd` federates three of four owners. BUGS `F35`.

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
- `audit/PLAN.md` — the master event list and category taxonomy behind
  the field schema below. (`specs/8/F-audit-stream.md` proposed an
  `ipc_audit` table; migration `0066` consolidated it into `audit_log` and
  `specs/8/index.md` marks it superseded by this spec.)
