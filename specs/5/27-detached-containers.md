---
status: draft
---

# Detached Containers

**Status**: planned

## Problem

Container↔gateway communication is coupled to the docker exec
process. `container.Run` attaches stdout/stderr via exec streams and
reads `---ARIZUKO_OUTPUT_START---` / `---ARIZUKO_OUTPUT_END---`
markers.

When gated restarts, the exec handle is gone. The container keeps
running but its output is unreadable — gated cannot receive pending
output or tell whether the container is healthy or stalled. Only
recovery today: kill orphaned containers on startup, re-spawn on
next message.

Problems:

1. In-flight responses are lost on restart.
2. Idle containers (waiting in `waitForIpcMessage`) are killed
   unnecessarily — they had no pending work and could have served
   the next message.

## Design

Use the IPC directory as the single channel for both directions.
Input is already file-based (`ipc/<folder>/input/*.json` + SIGUSR1).
This spec makes output file-based too.

### Container side (agent-runner)

`writeOutput(output)` writes a timestamped JSON file instead of
printing to stdout:

```
/workspace/ipc/output/<timestamp>-<uuid>.json
```

File written atomically (`.tmp` → rename). After writing, signals
gated via `kill -SIGUSR2 <gateway-pid>` where gateway PID is read
from `/workspace/ipc/gateway.pid`.

If `gateway.pid` is missing or stale, the container continues
normally — output files accumulate and gated drains them on
reconnect.

### Gateway side (gated)

**On spawn** (`container.Run`):

- Write own PID to `<ipc-dir>/gateway.pid`
- Spawn container with stdin for initial `ContainerInput` delivery,
  then closed
- Watch `<ipc-dir>/output/` for new `.json` files (500ms poll fallback)
- For each new file: parse output, call `onOutput`, delete file
- `state.process` kept only for timeout-kill (`docker kill <name>`)

**On restart** (startup reclaim):

1. `docker ps --filter name=arizuko-` → list running containers
2. Derive group folder from container name
3. For each live container: check `<ipc-dir>/output/` for unprocessed files
4. Drain output files → call normal output handlers
5. Register container as active in GroupQueue with file-watching
6. Mark as `idleWaiting` if output dir empty after drain

After reclaim, new messages flow via IPC input as normal. Containers
never notice the restart.

### GroupQueue changes

`RegisterProcess` gains an optional container-name-only path:
`ChildProcess` is optional for reclaimed containers.
`SignalContainer` already uses `docker kill --signal=SIGUSR1 <name>` —
no process handle needed.

`state.process` becomes `*exec.Cmd | nil`. Timeout enforcement uses
`docker kill <name>` directly.

## What stays the same

- IPC input: `ipc/<folder>/input/*.json` + SIGUSR1
- `_close` sentinel
- Container mounts and directory layout
- Timeout enforcement via `docker stop`/`docker kill`
- Session tracking

Note on IPC input injection: gateway-side steering uses
`SendMessages(jid, texts []string)` (batch API in `queue.go`), which
writes one IPC file per message and signals the container once.

On the container side, the `PostToolUse` hook (`createIpcDrainHook`) is
the exclusive message-drain path — it drains the IPC input directory
between tool calls and returns queued messages as
`hookSpecificOutput.additionalContext`, which the SDK appends to the
tool result Claude is about to read (mid-loop injection inside the
current turn). `pollIpcDuringQuery` now only handles the `_close`
sentinel; it no longer drains user messages. For text-only responses
where no tool fires, steered messages are picked up at the next query
start via `checkIpcMessage()`. Both `pollIpcDuringQuery` and the hook
go through `drainIpcInputMutex` — a `draining` boolean flag
(single-threaded JS guard, not an OS mutex) — so they never claim the
same files. The between-turn drain in `checkIpcMessage` runs
sequentially with the query and calls `drainIpcInput` directly, no
mutex required.

## What we gain

- Restart is non-destructive. In-flight agent responses survive.
- Idle container reclaim is trivial — no re-spawn needed.
- IPC dir is the single communication channel.
- Output stall detection: no output file within N minutes = stuck.

## Implementation

| File                     | Change                                            |
| ------------------------ | ------------------------------------------------- |
| `container/agent-runner` | `writeOutput` → write JSON file, read gateway.pid |
| `container/runner.go`    | Add `ipc/output/` watcher, remove stdout parser   |
| `queue/queue.go`         | Make process optional, add reclaim path           |
| `gateway/gateway.go`     | Add startup reclaim call after orphan scan        |

## Not in scope

- Output file retention for replay (delete after processing)
- Scenario/test mode changes (can continue using stdout markers)
