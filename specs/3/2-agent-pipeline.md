# Agent Orchestration & Workflows

**Status**: product config (no new gateway code)

Workflows are configuration on top of existing primitives:
`delegate_group`, `escalate_group`, `send_message`, slink POSTs.
See `specs/1/F-group-routing.md`, `specs/2/5-permissions.md`.

## Two concepts

### Orchestration (inter-agent messaging)

Long-lived groups messaging each other via slink. Each has own
session, memory, persona. Useful for: async background jobs,
persistent worker agents.

### Workflows (subagent pipeline)

Single group spawns subagents (Agent tool) within one container.
Shared context window, mounts, session. Useful for: multi-step
jobs where context must flow richly between steps.

## Comparison

|                 | Orchestration                     | Workflow                |
| --------------- | --------------------------------- | ----------------------- |
| Context sharing | explicit (payload/mount)          | implicit (same context) |
| Role isolation  | strong (own session/memory/tools) | none                    |
| Latency         | compounds per hop                 | single container        |
| Persistence     | workers remember across runs      | ephemeral               |
| Complexity      | high (slink, session handoff)     | low (Agent tool)        |

**Today**: workflows work now. Orchestration needs slink shipped.

## Implementation

No gateway changes. Workflows use Agent tool (Claude Code native).
Orchestration uses slink POSTs + `send_message` IPC for terminal hop.
Pipeline topology lives in orchestrator's skill files (product config).

## Related

- `specs/1/W-slink.md` — slink endpoint for inter-agent POSTs
- `specs/1/2-agent-teams.md` — why Claude agent teams don't work
