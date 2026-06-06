---
status: spec
---

# specs/6/F — Audit log

> Pairs with [`../7/3-git-as-truth.md`](../7/3-git-as-truth.md):
> SQLite audit tables defined here cover warm-tier decisions (every
> turn writes a sidecar referencing actor + action IDs from these
> tables), while cold-tier config writes are audited natively by git
> history. Together they form the complete audit surface — enterprise
> readable via SQL, distribution-ready via `git log`.
>
> Field schema for every audit row + per-tool-call slog line lives in
> [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md); this
> spec defines the DB table that consumes it.

## What this solves

Mutating platform actions need a queryable, append-only trail. The
SQLite `audit_log` table is the **source of truth**: every
state-changing operation writes one row inside the same DB transaction
as the resource mutation, so the trail is ACID and never lossy. The
slog stream (→ journald) is separate operational telemetry — high-rate,
interactive, lossy by design. If the two ever disagree, the table wins.

Today:

- `cli_audit` table (v0.42.0) — CLI mutations, OS user + redacted args
- `secret_use_log` table (spec Y M1) — per-call secret injection events
- proxyd already emits structured slog lines per request
- `messages`, `session_log`, `turn_results`, `task_run_logs` — message lifecycle

The gap: MCP tool mutations from agents (via `ipc/ipc.go`) and the
generalised platform write path are not in a unified table. Existing
tables (`cli_audit`, `secret_use_log`) fold into `audit_log` under the
field schema in [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md).

## Design

The DB table is the substrate; slog is observability on top. Two paths,
clearly split:

- **`audit_log` DB table** — source of truth for every state-changing
  operation (MCP + REST + CLI, all daemons). Written transactionally
  with the mutation; rollback on audit-write failure. Schema and field
  semantics: [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md).
- **slog** — operational telemetry for both state-changing and
  read-only calls. Ensures interactive ops ("what is the agent doing
  right now") work without polling SQLite per call. Lossy: journald
  rotation, level filtering. Not the audit substrate.

SIEM export is out of scope for now — operators query `audit_log`
directly (`sqlite3`) or tail the structured log. OTLP / SIEM exporters
are tee'd off slog later; the DB table stays canonical.

## audit_log table

The unified table — covers MCP tool calls, REST hits, CLI mutations.
Replaces the per-surface tables (`cli_audit`, `ipc_audit`) as one
source of truth. Field semantics in
[`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md).

```sql
CREATE TABLE IF NOT EXISTS audit_log (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  actor          TEXT NOT NULL,
  actor_sub      TEXT NOT NULL,
  tool           TEXT NOT NULL,
  surface        TEXT NOT NULL,  -- 'mcp' | 'rest' | 'cli'
  resource       TEXT NOT NULL,
  params_summary TEXT NOT NULL,  -- JSON; secrets redacted; ≤1 KB
  outcome        TEXT NOT NULL,  -- 'ok' | 'error'
  error          TEXT,           -- only when outcome='error'
  duration_ms    INTEGER,
  turn_id        TEXT,
  folder         TEXT NOT NULL,
  instance       TEXT NOT NULL
);
```

Written by the `resreg` adapter (and hand-rolled `ipc/ipc.go` handlers
that haven't migrated yet) in the SAME database transaction as the
resource mutation. The adapter — not the handler — calls
`audit.EmitInTx(tx, ...)` after the handler returns successfully, then
commits the tx; if the audit-row insert fails, the mutation rolls back.
Handlers don't have to remember; they only run their work against
`Execution.Tx`. Forwarder resources (`Resource.Store == nil`, e.g.
`webd/routes_mcp.go`) skip the local row — the downstream daemon
writes it. The full contract is in
[`../5/5` Execution context](../5/5-uniform-mcp-rest.md).
Per-mutating-tool list is the same set as
[`../5/5-uniform-mcp-rest.md`](../5/5-uniform-mcp-rest.md) "Resource
declarations to add" — every state-changing endpoint there.

Read-only tools (`inspect_*`, `list_*`, `get_*`, REST GETs) do **not**
write `audit_log` rows — slog only.

Authorization failures on mutating attempts (`outcome='denied'`,
`error_msg='forbidden'`) ARE recorded — primary signal for privilege
escalation. Authorization failures on read-only attempts are
slog-only (volume).

## cli_audit (existing, v0.42.0)

Already covers: `group add/rm/grant/ungrant`, `invite create/revoke`,
`secret set/delete`, `network allow/deny`, `token issue/revoke`,
`identity link/unlink`. `actor_sub` is `os_user` from the system.

Folds into `audit_log` (above) as `surface='cli'` rows once `5/D` M3
(CLI cutover to the local MCP socket) lands. Until then `cli_audit`
stays as-is and queries union both tables.

## secret_use_log (spec Y M1)

Per-injection record when `injectSecretsAdapter` resolves a secret into an MCP
tool call. Separate table because it is high-frequency (every tool call that
uses secrets) and per-turn, not per-mutation. Schema defined in spec Y.

## proxyd web access log

Already emits structured slog per request. Ensure `actor_sub` (the verified
`X-User-Sub` value after sig check, `""` if unauthenticated) is present as a
slog field. `/health` excluded. No new table — the journal is the record.

## Querying

```bash
# Recent mutations on krons (all surfaces)
sudo sqlite3 /srv/data/arizuko_krons/store/messages.db \
  "SELECT created_at, surface, folder, actor_sub, tool, outcome FROM audit_log ORDER BY id DESC LIMIT 50;"

# Legacy CLI mutations (pre-cutover)
sudo sqlite3 /srv/data/arizuko_krons/store/messages.db \
  "SELECT ts, os_user, command, args FROM cli_audit ORDER BY id DESC LIMIT 50;"
```

## What's out of scope

- File rotation, JSONL export, SIEM webhooks — add later if operators need them
- OTLP exporter — covered by [`../5/O-otlp-export.md`](../5/O-otlp-export.md); slog stream tee'd to OTLP, this table stays canonical
- Message content — never logged (PII)
- Agent tool-use telemetry (Bash, Edit, Read in-container) — emitted to
  slog under [`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md)
  Layer B, not into `audit_log` (it's operational, not platform audit)
- vited access log — static asset serving, no auth boundary, not a security surface
