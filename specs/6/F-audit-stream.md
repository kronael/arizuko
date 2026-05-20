---
status: spec
---

# specs/6/F — Audit stream (three-stream SIEM export)

## What this solves

Security teams at operator deployments need an append-only event trail for
Splunk, Datadog, or their own SIEM. Today audit data is scattered across SQLite
(`messages`, `session_log`, `turn_results`, `task_run_logs`, `secret_use_log`,
`cli_audit`) and proxyd's structured log. There is no unified, append-only,
file-based export path. This spec defines three distinct audit streams, each
written to its own JSONL file, with optional HTTP POST batching for remote ingest.

## Three streams

| Stream     | File                | What it records                                                      | Source                                                     |
| ---------- | ------------------- | -------------------------------------------------------------------- | ---------------------------------------------------------- |
| `system`   | `audit-system.jl`   | Platform mutations (group, grants, routes, secrets, invites, tokens) | MCP tool calls in `ipc/ipc.go`                             |
| `messages` | `audit-messages.jl` | Inbound/outbound messages, turn lifecycle, errors                    | `messages`, `session_log`, `turn_results`, `task_run_logs` |
| `web`      | `audit-web.jl`      | HTTP requests to proxyd                                              | proxyd request middleware                                  |

All three files live under `$HOST_DATA_DIR/`. Tool-use audit (bash, read_file,
etc.) is agent-internal observability and is out of scope.

---

## Stream 1 — System audit

### Purpose

Who changed what in the platform. A CISO must be able to answer: which actor
created or deleted a group, modified grants/ACL, set or deleted a secret, issued
or revoked a token, or changed routing — and when.

### Event schema

```json
{
  "id": "e3b0c44298fc1c149a",
  "ts": "2026-05-19T09:12:00Z",
  "stream": "system",
  "instance": "krons",
  "actor_sub": "telegram:user/99123",
  "tool": "register_group",
  "folder": "corp/eng",
  "params": { "jid": "telegram:group/456", "fromPrototype": false },
  "outcome": { "status": "ok" }
}
```

| Field       | Type   | Description                                                                        |
| ----------- | ------ | ---------------------------------------------------------------------------------- |
| `id`        | string | random UUID v4; idempotency key for SIEM deduplication on retry                    |
| `ts`        | string | RFC3339 UTC, from wall clock at call time                                          |
| `stream`    | string | always `"system"`                                                                  |
| `instance`  | string | `$INSTANCE_NAME`                                                                   |
| `actor_sub` | string | identity header from ipc session; `"operator-cli"` for CLI invocations             |
| `tool`      | string | MCP tool name or REST endpoint that caused the mutation                            |
| `folder`    | string | affected group folder; empty for instance-wide mutations                           |
| `params`    | object | sanitised tool arguments (no secret values — see below)                            |
| `outcome`   | object | `{"status":"ok\|error\|authz_denied","detail":"..."}` — `detail` present on non-ok |

### Covered mutations

These MCP tools and REST paths emit system events:

| Tool / endpoint                             | What changed                      |
| ------------------------------------------- | --------------------------------- |
| `register_group`                            | group created                     |
| `set_routes` / `add_route` / `delete_route` | routing table changed             |
| `invite_create`                             | invite token issued               |
| `revoke_token`                              | chat/webhook token revoked        |
| `issue_chat_link` / `issue_webhook`         | token issued                      |
| `set_group_open`                            | group open/closed state changed   |
| `set_observe_window`                        | observe window changed            |
| `set_web_route` / `del_web_route`           | web route changed                 |
| `schedule_task`                             | scheduled task created or updated |
| dashd `PUT /me/secrets/:key`                | user secret set                   |
| dashd `DELETE /me/secrets/:key`             | user secret deleted               |
| CLI `arizuko secret set/delete`             | secret set or deleted via CLI     |
| CLI `arizuko group add/rm/grant/ungrant`    | group or grant mutated via CLI    |
| CLI `arizuko invite create/revoke`          | invite token issued or revoked    |
| CLI `arizuko network allow/deny`            | egress rule changed               |
| CLI `arizuko token issue/revoke`            | chat/webhook token issued/revoked |
| CLI `arizuko identity link/unlink`          | identity linked or unlinked       |

ACL/grants writes (`UpsertACL`, `DeleteACL`) are currently gated behind dashd
and `arizuko grant` — those paths must also emit system events at the store call
site (not the HTTP handler) so CLI and dashboard paths both record.

**Authorization failures** are also recorded: when `AuthorizeStructural` returns
an error (tier too low, folder out of scope), the call site emits a system event
with `outcome.status=authz_denied` and the actual `detail`. This is the primary
signal for privilege-escalation detection.

**Secret value redaction**: `params` must never contain the secret value. For
`secret set`, include `{key, scope, scope_id}` only. Outcome records success/error.

### Source

Events are emitted synchronously at the MCP tool call return site in
`ipc/ipc.go` (after auth checks, whether the call succeeds or fails). Each
write path calls a shared `audit.EmitSystem(ctx, event)` function that appends
to `audit-system.jl` immediately (no batching delay — system events are
low-frequency and high-priority). Webhook delivery for system events is also
immediate, not batched: each event is POSTed individually when a webhook URL is
configured. A no-op guard when `AUDIT_ENABLED=false` returns immediately with
no allocation.

CLI mutations emit via two paths:

- **`audit.EmitSystem` at the store write site** — for mutations that go through
  `store.*` directly (secrets, grants). No call site missed.
- **`cli_audit` DB table** (migration 0061, shipped v0.42.0) — mutating CLI
  commands (`group add/rm/grant`, `invite create/revoke`, `secret set/delete`,
  `network allow/deny`, `token issue/revoke`, `identity link/unlink`) written
  by `cmd/arizuko/main.go` with OS user + redacted args. The system-stream
  poll exporter reads `cli_audit` exactly like other source tables (cursor on
  `id`), mapping each row to a system event with `actor_sub="cli:<os_user>"`.

dashd REST mutations (secrets, routes, ACL) emit at the handler level with the
authenticated user sub from the request context.

---

## Stream 2 — Message audit

### Purpose

Inbound/outbound message record, turn lifecycle, scheduled task runs. The trail
for "what did the agent receive and send" and "when did sessions start and end".

### Event schema

```json
{
  "id": "7f83b1657ff1fc53b9",
  "ts": "2026-05-18T10:04:31Z",
  "stream": "messages",
  "instance": "krons",
  "folder": "solo/inbox",
  "chat_jid": "telegram:user/99123",
  "actor": "telegram:user/99123",
  "action": "message_in",
  "params": {
    "verb": "message",
    "source": "teled",
    "topic": "",
    "msg_id": "abc123"
  },
  "outcome": { "status": "ok" }
}
```

| Field      | Type   | Description                                                         |
| ---------- | ------ | ------------------------------------------------------------------- |
| `id`       | string | `"<table>:<row_id>"` — stable, deduplication key                    |
| `ts`       | string | RFC3339 UTC from source row                                         |
| `stream`   | string | always `"messages"`                                                 |
| `instance` | string | `$INSTANCE_NAME`                                                    |
| `folder`   | string | group folder path                                                   |
| `chat_jid` | string | typed JID of the chat; empty if not applicable                      |
| `actor`    | string | sender JID for inbound; `"bot"` for agent-generated                 |
| `action`   | string | see enum below                                                      |
| `params`   | object | action-specific fields (no message body — PII boundary)             |
| `outcome`  | object | `{"status":"ok\|error","detail":"..."}` — `detail` present on error |

### Action enum and source tables

| Action        | Source table     | Key columns                                                                                     |
| ------------- | ---------------- | ----------------------------------------------------------------------------------------------- |
| `message_in`  | `messages`       | `timestamp`, `chat_jid`, `sender`, `verb`, `source`, `topic`, `id`; `is_from_me=0`, `errored=0` |
| `message_out` | `messages`       | same; `is_from_me=1`, `errored=0`                                                               |
| `error`       | `messages`       | same; `errored=1`                                                                               |
| `turn_start`  | `session_log`    | `started_at`, `group_folder`, `session_id`, `message_count`                                     |
| `turn_end`    | `turn_results`   | `recorded_at`, `folder`, `turn_id`, `session_id`, `status`                                      |
| `task_run`    | `task_run_logs`  | `run_at`, `task_id`, `duration_ms`, `status`, `error`                                           |
| `secret_use`  | `secret_use_log` | `ts`, `folder`, `caller_sub`, `tool`, `key`, `scope`, `status`, `latency_ms`                    |

`secret_use` records are secret-resolution events (read-only, no mutation) and
belong with the message stream because they are per-turn agent runtime events
correlated by `folder` + session. Secret mutation events (set/delete) are in the
system stream.

`chat_jid` and `folder` for `task_run` come from joining `task_run_logs` →
`scheduled_tasks.group_folder`. Where a table has no `chat_jid`, the field is
`""`.

Message `content` is never included in `params`. `sender` JID is included as-is;
operators are responsible for field-level PII redaction downstream.

### Poll-based export

The message-stream exporter runs as a goroutine in `gated` (all source tables
are in the same `messages.db`). No new daemon. Poll every 10 seconds:

```
for each source table:
    SELECT rows WHERE id > cursor[table] ORDER BY id LIMIT 500
    for each row: build event, append to audit-messages.jl, buffer for webhook
    cursor[table] = last row id seen
flush webhook batch if len >= 200 or last flush > 5s
persist cursors to $HOST_DATA_DIR/audit-cursor.json
```

`audit-cursor.json` maps table name → last exported integer ID. The system
stream polls `cli_audit` (integer `id` PK) in addition to MCP-side events.
Tables without an integer PK (`secret_use_log` uses `ts`) use an ISO timestamp

- row-count sentinel. Loaded at startup; created fresh (all-zeros) if absent, so first run
  exports from the oldest row in each table.

**Cursor loss causes duplicate events.** If `audit-cursor.json` is deleted or
corrupted, the next start re-exports from row 0. This is by design (no data
loss). Downstream SIEM must deduplicate on the `id` field (present on all
events). The spec does not attempt to prevent duplicates at source — idempotent
ingest is the correct fix.

---

## Stream 3 — Web access log

### Purpose

HTTP requests to proxyd: who, when, what path, response code, latency. Standard
structured log compatible with Apache/nginx log parsers and SIEM file tails.

### Event schema

One JSON object per line, written by proxyd's `logging` middleware:

```json
{
  "ts": "2026-05-19T09:12:00.123Z",
  "stream": "web",
  "instance": "krons",
  "method": "POST",
  "path": "/api/chat/abc123",
  "status": 200,
  "latency_ms": 47,
  "actor_sub": "telegram:user/99123",
  "ip": "1.2.3.4"
}
```

| Field        | Type    | Description                                                         |
| ------------ | ------- | ------------------------------------------------------------------- |
| `ts`         | string  | RFC3339 UTC with millisecond precision                              |
| `stream`     | string  | always `"web"`                                                      |
| `instance`   | string  | `$INSTANCE_NAME`                                                    |
| `method`     | string  | HTTP verb                                                           |
| `path`       | string  | URL path only, no query string (avoids logging tokens)              |
| `status`     | integer | HTTP response code                                                  |
| `latency_ms` | integer | handler duration in milliseconds                                    |
| `actor_sub`  | string  | `X-User-Sub` header after sig check; `""` if unauthenticated        |
| `ip`         | string  | client IP after `X-Forwarded-For` processing (trusted proxies only) |

Query strings are excluded from `path` to avoid logging bearer tokens that some
clients pass in URLs. The existing proxyd `logging` middleware writes to slog
today; this adds a parallel write to `audit-web.jl` in the same handler.

`/health` requests are excluded from `audit-web.jl` — Docker healthcheck polls
every few seconds and would otherwise dominate the log with noise.

Web events are written synchronously per request — no cursor/poll needed.

---

## Transport

**Local file (default):** append to stream-specific file under `$HOST_DATA_DIR/`.
Rotate when file exceeds `AUDIT_MAX_BYTES` (default 100 MB) or `AUDIT_ROTATE_HOURS`
(default 24 h), whichever comes first. Rotate by renaming to
`<file>.<unix-timestamp>`; no compression. Keep the two most recent rotated files.

**HTTP POST webhook (optional):** when `AUDIT_WEBHOOK_URL` is set, batch events
(up to 200 lines) and POST as `application/x-ndjson`. Include
`Authorization: Bearer <AUDIT_WEBHOOK_SECRET>` when set. Retry up to 3 times
with exponential backoff on non-2xx. On failure, log and skip the batch — the
local file is the durable record.

File and webhook run together. Webhook is per-stream when
`AUDIT_WEBHOOK_URL_SYSTEM`, `AUDIT_WEBHOOK_URL_MESSAGES`, or
`AUDIT_WEBHOOK_URL_WEB` are set; `AUDIT_WEBHOOK_URL` applies to all three as
a fallback.

**Out of scope:** native Splunk HEC, Datadog agent — use the JSONL files with a
standard file forwarder (Splunk UF, Datadog Agent file tail, Fluentd, Vector).

---

## Config

| Env var                      | Default     | Description                                     |
| ---------------------------- | ----------- | ----------------------------------------------- |
| `AUDIT_ENABLED`              | `false`     | set `true` to enable all streams                |
| `AUDIT_MAX_BYTES`            | `104857600` | rotate file at this size (bytes)                |
| `AUDIT_ROTATE_HOURS`         | `24`        | rotate file after this many hours               |
| `AUDIT_WEBHOOK_URL`          | `""`        | HTTP POST endpoint for all streams              |
| `AUDIT_WEBHOOK_URL_SYSTEM`   | `""`        | override webhook for system stream              |
| `AUDIT_WEBHOOK_URL_MESSAGES` | `""`        | override webhook for messages stream            |
| `AUDIT_WEBHOOK_URL_WEB`      | `""`        | override webhook for web stream                 |
| `AUDIT_WEBHOOK_SECRET`       | `""`        | `Authorization: Bearer` value for webhook POSTs |

Output paths are `$HOST_DATA_DIR/audit-{system,messages,web}.jl` — not
configurable individually; they follow the instance data directory convention.

---

## What's out of scope

- **PII redaction**: `sender` JID is included as-is; message `content` is never
  included. Operators are responsible for downstream field-level redaction.
- **Retroactive export**: cursor starts at row 0 on first run, so historical rows
  ARE exported unless the operator primes `audit-cursor.json` manually.
- **Encryption at rest**: use filesystem-level encryption or an encrypting
  forwarder. The files are append-only and readable by the OS user running gated.
- **Per-folder filtering**: all folders export or none.
- **Agent tool-use**: bash, read_file, write_file and other Claude Code tool
  calls are agent-internal and not recorded here.
- **Integrity protection**: the files are not signed or hash-chained. Operators
  who need tamper-evidence should forward to an immutable SIEM backend promptly.
  The local file is not a substitute for remote, append-only storage.
