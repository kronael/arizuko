---
status: deferred
---

# HITL firewall — human-in-the-loop tool gate

Firewall between MCP socket and tool handlers. Held tools write a
`pending_actions` row (status `held`) and return
`{pending: true, id: "pa-..."}` to the agent. Operator reviews via
`/dash/review` and approves/rejects; a dispatcher executes approved
calls and writes `result`/`error` back; agent fetches resolution via
`get_pending_action(id)` or next-turn injection.

## Schema

```
pending_actions(id TEXT PK, group_folder, caller_agent, tool,
  args JSON, status, created_at, reviewed_by, reviewed_at,
  reviewer_note, result JSON, error)
```

## Grant markers

`hold: true` always queues; `hold_if: <cel>` conditional. Absent = execute
inline (current behaviour).

## Hooks

Existing primitives only: `grants/` (new markers), `ipc/` (intercept +
queue + return pending), `dashd` (new review screen), `scheduled_tasks`
(optional: approve-now-execute-later).

## Open

- Dispatcher location: `gated` goroutine vs dedicated `holdd`
- Execution identity: original agent's grants vs reviewer's
- Per-tool timeouts and edited-args audit trail
- Pending-awareness injection back into agent
- Non-MCP callers (`timed`) bypass path
