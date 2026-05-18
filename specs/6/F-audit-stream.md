---
status: draft
---

# specs/6/F — Audit stream (SIEM export)

## What this solves

All audit data exists in SQLite (`messages`, `session_log`, `task_run_logs`,
`turn_results`, `secret_use_log`) but there is no structured stream to an
external SIEM. Enterprise security teams require events in Splunk/Datadog/etc.

## Scope

- Append-only JSONL export: one line per action (message received, turn
  started, tool called, secret accessed, outbound sent, error)
- Each line: `{ts, instance, folder, chat_jid, actor, action, params, outcome}`
- Transport: local file rotation (default) + optional HTTP POST webhook
- `gated` tails the DB change log and emits; no daemon changes needed

## Not in scope

- Native Splunk HEC / Datadog agent integration (use file → forwarder)
- Retroactive export of existing history
- PII redaction (operator responsibility)

## Open questions

- Pull from SQLite WAL via polling or via a trigger/hook in store write path?
- Rotation policy (size vs time)?
- Which actions are mandatory vs optional in the schema?
