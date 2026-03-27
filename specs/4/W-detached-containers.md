---
status: draft
---

## <!-- trimmed 2026-03-15: TS migration removed, rich facts only -->

## status: planned

# Detached Containers

## Problem

Container-gateway communication coupled to docker stdio pipe.
Gateway restart loses ChildProcess handle; stdout unreadable.
In-flight responses lost. Idle containers killed unnecessarily.

## Design

IPC directory as single communication channel for both directions.
Input already file-based. This makes output file-based too.

### Container Side

`writeOutput(output)` writes timestamped JSON:
`/workspace/ipc/output/<timestamp>-<uuid>.json`

Atomic write (`.tmp` -> rename). Signals gateway via
`kill -SIGUSR2 <gateway-pid>` (read from `/workspace/ipc/gateway.pid`).
If PID missing/stale, continues normally -- files accumulate, gateway
drains on reconnect.

### Gateway Side

**On spawn**: write PID to `gateway.pid`, watch `output/` for new
JSON files (fs.watch + 500ms poll fallback), parse ContainerOutput,
call onOutput, delete file.

**On restart (reclaim)**:

1. `docker ps --filter name=arizuko-` -> list running containers
2. Derive folder from container name
3. Drain unprocessed output files -> call output handlers
4. Register in GroupQueue with file-watching (no ChildProcess needed)
5. New messages flow via IPC input as normal

Containers never notice gateway restarted.

### GroupQueue Changes

`state.process` becomes `ChildProcess | null`. Null is valid for
reclaimed containers. Timeout uses `docker kill <name>` directly.

## What We Gain

- Gateway restart non-destructive: in-flight responses survive
- Idle container reclaim: no re-spawn needed
- Stall detection: no output file in N minutes = stuck
- IPC dir is single communication channel

## What We Lose

- Real-time container stderr (debug-only, acceptable)

## What Stays the Same

- IPC input (`input/*.json` + SIGUSR1)
- `_close` sentinel
- Container mounts and directory layout
- Initial stdin for secrets delivery
- Timeout enforcement (`docker stop` -> `docker kill`)
- Session tracking

## Open Questions

- gateway.pid vs inotify: gateway.pid preferred (avoids inotify
  limitations on docker bind mounts)
- Output file retention: delete after processing (simpler, no replay
  needed since gateway drains on restart)
- Scenario mode: prefer file-based too (full coverage)
