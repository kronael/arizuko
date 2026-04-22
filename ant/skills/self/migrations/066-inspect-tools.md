# 066 — inspect-tools MCP family

New read-only MCP tools for runtime introspection:

- `inspect_messages` — local DB rows for a JID (already existed; now
  documented as part of the family)
- `inspect_routing` — routes visible to this group, JID→folder
  resolution, and per-chat errored-message aggregate
- `inspect_tasks` — scheduled_tasks visible to this group, plus recent
  `task_run_logs` when `task_id` is supplied
- `inspect_session` — current session id + recent `session_log` entries
  (message count, last error, last context reset)

Dropped from v1: `inspect_logs`, `inspect_health`. Those need
journal / docker-socket access the agent container doesn't have today.

Tier gating: tier 0 sees all instances; tier ≥1 sees only its own
folder subtree.

Use these instead of `Bash sqlite3 …` / `Bash journalctl …` for state
the router DB already holds.
