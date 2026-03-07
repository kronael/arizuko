# Agent Teams IPC Analysis — shipped

## How Claude Code Agent Teams Communicate

The coordination bus is purely **filesystem-based**. No network, no daemon.

### Shared task list

- Path: `~/.claude/tasks/{team-name}/` — one JSON file per task
- Teammates poll this directory to find unclaimed work
- File locking prevents race conditions when multiple teammates claim simultaneously
- Task states: pending → in_progress → completed
- Dependencies tracked in task files — blocked tasks unblock automatically

### Mailbox (SendMessage)

- Path: `~/.claude/teams/{team-name}/` — message files per recipient
- `SendMessage` writes a file to the recipient's mailbox directory
- Recipient polls and consumes messages
- Lead gets idle notifications when teammates finish

Both mechanisms: **file I/O + file locking**. Same pattern as kanipi's own
`/workspace/ipc/messages/` polling.

## Why Agent Teams Break in Kanipi

### The stdio problem

Gateway spawns one container → one stdio pair (stdin/stdout). The gateway reads
agent output from that single stdout pipe. Agent teams spawn **sibling processes**
inside the container — each with their own stdio. Those sibling stdouts go
nowhere: gateway never reads them. Any result, IPC message, or channel reply
from a teammate is silently dropped.

### The path problem

`~/.claude/teams/` and `~/.claude/tasks/` resolve to `/home/node/.claude/`
inside the container, which is mounted from `/data/sessions/{group}/.claude/`.
This is scoped per-group — correct for a single agent session, wrong for
multi-teammate coordination across the team lifecycle (teams persist, containers
don't).

### Summary

| Issue               | Detail                                                               |
| ------------------- | -------------------------------------------------------------------- |
| Teammate stdout     | Goes nowhere — gateway reads only parent container's pipe            |
| SendMessage to user | No path back to channel (Telegram/WhatsApp/Discord)                  |
| Team persistence    | `~/.claude/teams/` lives in per-group session dir, wiped or reseeded |
| Orphan processes    | No cleanup when parent container exits (idle timeout / error)        |

## Potential Future Resolution

If kanipi ever wanted multi-agent coordination within a group, the correct
approach would be to implement it at the **gateway level** using kanipi's
existing file-based IPC (`/workspace/ipc/`):

- Each "agent role" = separate registered group with its own container
- Coordination messages go through the gateway IPC watcher (already polling)
- Gateway routes inter-agent messages without needing agent teams at all
- Each container gets proper stdio, lifecycle management, idle timeout

This is essentially what kanipi's multi-group architecture already is —
each group is an independently managed agent. Agent teams would be redundant
and broken on top of it.
