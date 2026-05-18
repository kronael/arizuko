---
status: draft
---

# specs/6/F — Audit stream (SIEM export)

## What this solves

Security teams at operator-managed deployments need an append-only event
trail to feed into Splunk, Datadog, or their own SIEM. Today all audit data
exists in SQLite (`messages`, `session_log`, `task_run_logs`, `turn_results`,
`secret_use_log`, `cost_log`) but there is no structured export path — an
operator must run raw SQL or scrape gated logs. This spec defines a JSONL
exporter that tails those tables and writes one event line per action, with
optional HTTP POST batching for remote ingest.

## Event schema

Each line is a JSON object, newline-terminated, with these fields:

| Field      | Type   | Description                                              |
| ---------- | ------ | -------------------------------------------------------- |
| `ts`       | string | RFC3339 UTC timestamp of the source event                |
| `instance` | string | `$INSTANCE_NAME` env var                                 |
| `folder`   | string | group folder path (empty for instance-wide events)       |
| `chat_jid` | string | typed JID of the chat (`telegram:group/123`, `` if N/A)  |
| `actor`    | string | `sender` JID for inbound; `bot` for agent-generated      |
| `action`   | string | enum — see below                                         |
| `params`   | object | action-specific fields                                   |
| `outcome`  | object | `{"status":"ok"}` or `{"status":"error","detail":"..."}` |

### Action enum

| Action        | Source table     | Meaning                         |
| ------------- | ---------------- | ------------------------------- |
| `message_in`  | `messages`       | inbound message stored          |
| `turn_start`  | `session_log`    | agent session started           |
| `turn_end`    | `turn_results`   | agent turn completed            |
| `tool_call`   | `secret_use_log` | secret resolved for a tool call |
| `task_run`    | `task_run_logs`  | scheduled task executed         |
| `message_out` | `messages`       | outbound message stored         |
| `error`       | `messages`       | message with `errored=1`        |

### Example lines

```jsonl
{"ts":"2026-05-18T10:04:31Z","instance":"krons","folder":"solo/inbox","chat_jid":"telegram:user/99123","actor":"telegram:user/99123","action":"message_in","params":{"verb":"message","source":"teled","topic":"","msg_id":"abc123"},"outcome":{"status":"ok"}}
{"ts":"2026-05-18T10:04:32Z","instance":"krons","folder":"solo/inbox","chat_jid":"telegram:user/99123","actor":"bot","action":"turn_start","params":{"session_id":"sess_xyz","message_count":1},"outcome":{"status":"ok"}}
{"ts":"2026-05-18T10:04:35Z","instance":"krons","folder":"solo/inbox","chat_jid":"","actor":"bot","action":"tool_call","params":{"tool":"get_secret","key":"OPENAI_API_KEY","scope":"folder","latency_ms":3},"outcome":{"status":"ok"}}
{"ts":"2026-05-18T10:04:41Z","instance":"krons","folder":"solo/inbox","chat_jid":"telegram:user/99123","actor":"bot","action":"turn_end","params":{"session_id":"sess_xyz","turn_id":"turn_456","status":"ok"},"outcome":{"status":"ok"}}
```

## Source tables

| Action        | Table            | Key columns used                                                                                |
| ------------- | ---------------- | ----------------------------------------------------------------------------------------------- |
| `message_in`  | `messages`       | `timestamp`, `chat_jid`, `sender`, `verb`, `source`, `topic`, `id`, `is_from_me=0`, `errored=0` |
| `message_out` | `messages`       | same, `is_from_me=1`, `errored=0`                                                               |
| `error`       | `messages`       | same, `errored=1`                                                                               |
| `turn_start`  | `session_log`    | `started_at`, `group_folder`, `session_id`, `message_count`                                     |
| `turn_end`    | `turn_results`   | `recorded_at`, `folder`, `turn_id`, `session_id`, `status`                                      |
| `tool_call`   | `secret_use_log` | `ts`, `folder`, `caller_sub`, `tool`, `key`, `scope`, `status`, `latency_ms`                    |
| `task_run`    | `task_run_logs`  | `run_at`, `task_id`, `duration_ms`, `status`, `error`                                           |

Each table has an integer primary key or a sortable `ts`/`recorded_at` column.
The exporter tracks the last exported row ID (or last exported `ts`) per table
in a `audit_cursor` file (see Config) so it can resume after restart without
re-emitting.

`chat_jid` and `folder` for `tool_call` and `task_run` events come from the
row directly (`secret_use_log.folder`, `task_run_logs.task_id` joined to
`scheduled_tasks.group_folder`). Where the table has no `chat_jid`, the field
is left empty (`""`).

## Transport

**Default — local file:**
Append to `<HOST_DATA_DIR>/audit.jsonl`. Rotate when the file exceeds
`AUDIT_MAX_BYTES` (default 100 MB) or `AUDIT_ROTATE_HOURS` hours (default 24),
whichever comes first. Rotate by renaming to `audit.jsonl.<unix-timestamp>`;
no compression (operators pipe through their own forwarder). Keep the two most
recent rotated files; delete older ones.

**Optional — HTTP POST webhook:**
When `AUDIT_WEBHOOK_URL` is set, batch completed events (up to 200 lines) and
POST them as `application/x-ndjson` to the URL. Include
`Authorization: Bearer <AUDIT_WEBHOOK_SECRET>` when `AUDIT_WEBHOOK_SECRET` is
set. Retry up to 3 times with exponential backoff on non-2xx. If all retries
fail, log the error and skip the batch (file is the durable record).

File and webhook run together — file acts as the local durable copy; webhook
as the push path to the SIEM.

**Out of scope:** native Splunk HEC, Datadog agent integration — use the JSONL
file with a standard file forwarder (Splunk UF, Datadog Agent file tail,
Fluentd).

## Implementation sketch

Run the exporter as a goroutine inside `gated`, started from `main()` alongside
the existing poll goroutines. No new daemon.

Justification: all source tables are in the same `messages.db` that `gated`
already holds open. A separate `auditd` would need another DB handle and a
coordination mechanism for WAL checkpoints. The exporter is read-only and
lightweight; its 10-second poll adds no write contention.

**Poll loop (every 10 seconds):**

```
for each source table:
    SELECT rows WHERE id > cursor[table] ORDER BY id LIMIT 500
    for each row:
        build event line
        append to audit.jsonl (buffered writer, flush per batch)
        append to webhook batch
    cursor[table] = last row id seen
flush webhook batch if len >= 200 or last flush > 5s ago
persist cursors to <HOST_DATA_DIR>/audit_cursor.json
```

`audit_cursor.json` maps table name → last exported integer ID (or ISO timestamp
for tables without an integer PK, e.g. `secret_use_log` uses `ts` + row count).
Loaded at startup; created fresh (all zeros) if absent, meaning export starts
from the oldest row in each table at first run.

The goroutine exits on context cancellation (SIGTERM) after flushing the current
batch.

## Config

| Env var                | Default              | Description                              |
| ---------------------- | -------------------- | ---------------------------------------- |
| `AUDIT_ENABLED`        | `false`              | set `true` to enable the exporter        |
| `AUDIT_MAX_BYTES`      | `104857600` (100 MB) | rotate output file at this size          |
| `AUDIT_ROTATE_HOURS`   | `24`                 | rotate output file after this many hours |
| `AUDIT_WEBHOOK_URL`    | `` (empty)           | optional HTTP POST endpoint              |
| `AUDIT_WEBHOOK_SECRET` | `` (empty)           | `Authorization: Bearer` value            |

Output path is `$HOST_DATA_DIR/audit.jsonl` — not configurable; follows the
instance data directory convention. `INSTANCE_NAME` (already required) populates
the `instance` field on every event.

## What's out of scope

- PII redaction — message `content` is not included in `params`; `sender` JID
  is included as-is. Operators are responsible for any field-level redaction.
- Retroactive export of pre-activation history — cursor starts at row 0 on
  first run, so historical rows ARE exported on first start unless the operator
  primes the cursor file manually.
- Encryption of `audit.jsonl` — use filesystem-level encryption or pipe through
  a forwarder that encrypts in transit.
- Per-folder audit filtering — all folders export or none do.
