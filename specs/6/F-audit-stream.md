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

## What this solves

Mutating platform actions need a queryable, append-only trail. Today:

- `cli_audit` table (v0.42.0) — CLI mutations, OS user + redacted args
- `secret_use_log` table (spec Y M1) — per-call secret injection events
- proxyd already emits structured slog lines per request
- `messages`, `session_log`, `turn_results`, `task_run_logs` — message lifecycle

The gap: MCP tool mutations from agents (via `ipc/ipc.go`) are not recorded.
Everything else is either already in the DB or in the normal log stream.

## Design

No new files, no JSONL rotation, no cursors. Two paths:

- **DB table** for low-frequency, queryable mutations (`cli_audit` already;
  add `ipc_audit` for MCP tool calls). Schema matches `cli_audit`.
- **slog** for high-frequency access events (proxyd request log — already
  emitted; ensure `actor_sub` field is present after auth check).

SIEM export is out of scope for now — operators query the DB tables directly
(`sqlite3`) or tail the structured log. Add an HTTP export endpoint later if
needed.

## ipc_audit table

Migration `0062-ipc-audit.sql`:

```sql
CREATE TABLE IF NOT EXISTS ipc_audit (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  ts      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  folder  TEXT NOT NULL,
  sub     TEXT NOT NULL,
  tool    TEXT NOT NULL,
  params  TEXT NOT NULL,
  outcome TEXT NOT NULL
);
```

- `sub` — caller identity from MCP session (`ARIZUKO_LOCAL_SUB` or remote sub)
- `tool` — MCP tool name
- `params` — JSON, secret values redacted (same rule as `cli_audit`)
- `outcome` — `"ok"` or `"error: <msg>"` or `"authz_denied"`

Written by `ipc/ipc.go` at the tool call return site for every mutating tool
(same list as the `cli_audit` CLI commands, plus: `register_group`,
`set_routes`, `add_route`, `delete_route`, `invite_create`, `revoke_token`,
`issue_chat_link`, `issue_webhook`, `set_group_open`, `set_observe_window`,
`schedule_task`, all grants/ACL writes). Read-only tools (`inspect_*`,
`list_*`, `get_*`) are not logged.

Authorization failures (`authz_denied`) are recorded regardless of whether
the call was mutating — they are the primary signal for privilege escalation.

## cli_audit (existing, v0.42.0)

Already covers: `group add/rm/grant/ungrant`, `invite create/revoke`,
`secret set/delete`, `network allow/deny`, `token issue/revoke`,
`identity link/unlink`. `actor_sub` is `os_user` from the system.

No changes needed.

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
# Recent MCP mutations on krons
sudo sqlite3 /srv/data/arizuko_krons/store/messages.db \
  "SELECT ts, folder, sub, tool, outcome FROM ipc_audit ORDER BY id DESC LIMIT 50;"

# CLI mutations
sudo sqlite3 /srv/data/arizuko_krons/store/messages.db \
  "SELECT ts, os_user, command, args FROM cli_audit ORDER BY id DESC LIMIT 50;"
```

## What's out of scope

- File rotation, JSONL export, SIEM webhooks — add later if operators need them
- Message content — never logged (PII)
- Agent tool-use (bash, read_file) — agent-internal, not platform audit
- vited access log — static asset serving, no auth boundary, not a security surface
