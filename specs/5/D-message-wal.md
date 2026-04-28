---
status: superseded
---

# Message WAL

Superseded by [27-detached-containers.md](27-detached-containers.md).
The MCP `submit_turn` collapse makes inbound durability fall out of
idempotent `submit_turn(turn_id)` calls — no separate WAL table needed.

Write-ahead log in `pending_delivery(id, group_folder, message_id,
written_at, acked_at)`. Pipe path writes WAL row before IPC file,
advances cursor only on agent ack. Crash before ack → next spawn
replays unacked. Agent dedups by message ID.

Rationale: current pipe-to-running-container advances cursor on IPC
write, losing messages on container crash. Not-advancing = guaranteed
duplicates on every pipe. WAL picks the right tradeoff.

Unblockers: add MessageID to IPC format, agent-side ack protocol, WAL
table + cleanup. Only matters when pipe volume is high and crashes
aren't rare.
