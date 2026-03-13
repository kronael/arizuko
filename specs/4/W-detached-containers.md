---
status: planned
---

# Detached Containers

## Problem

Container↔gateway communication is coupled to docker's stdio pipe.
`runAgentMode` spawns `docker run` with `stdio: ['pipe', 'pipe', 'pipe']`
and reads `---NANOCLAW_OUTPUT_START---` / `---NANOCLAW_OUTPUT_END---`
markers from the stdout ChildProcess stream.

When the gateway restarts, the ChildProcess handle is gone. The container
keeps running but its stdout is unreadable — the gateway cannot receive
pending output or tell whether the container is healthy or stalled. The
only recovery today is to kill orphaned containers on startup and re-spawn
on the next message.

This is fragile for two reasons:

1. In-flight responses are lost when the gateway restarts.
2. Idle containers (waiting in `waitForIpcMessage`) are killed unnecessarily
   — they had no pending work and would have served the next message fine.

## Design

Use the IPC directory (`/workspace/ipc` in-container, `/srv/data/<instance>/data/ipc/<folder>`
on host) as the single communication channel for both directions.

Input is already file-based (`/workspace/ipc/input/*.json` + SIGUSR1).
This spec makes output file-based too.

### Container side (`agent-runner`)

`writeOutput(output)` writes a timestamped JSON file instead of printing
to stdout:

```
/workspace/ipc/output/<timestamp>-<uuid>.json
```

File is written atomically (`.tmp` → rename). After writing, the container
signals the gateway via `kill -SIGUSR2 <gateway-pid>` where the gateway PID
is read from `/workspace/ipc/gateway.pid`.

If `gateway.pid` is missing or stale, the container continues normally —
output files accumulate and the gateway drains them on reconnect.

The `_close` sentinel (`/workspace/ipc/input/_close`) and SIGUSR1 wakeup
stay exactly as they are.

### Gateway side

**On spawn** (`runAgentMode`):

- Write own PID to `<ipc-dir>/gateway.pid`
- Spawn container with stdin closed (or still used for initial secrets delivery,
  then closed)
- Watch `<ipc-dir>/output/` for new `.json` files using `fs.watch` with
  500ms poll fallback (same pattern as current IPC input)
- For each new file: parse `ContainerOutput`, call `onOutput`, delete file
- `state.process` is kept only for timeout-kill (`docker kill <name>`) —
  it is no longer needed for stdout reading

**On gateway restart** (startup reclaim):

1. `docker ps --filter name=nanoclaw-` → list running containers
2. Derive group folder from container name (`nanoclaw-atlas-support-<ts>` → `atlas/support`)
3. For each live container: check `<ipc-dir>/output/` for unprocessed files
4. Drain output files → call normal output handlers (send messages to channel)
5. Register container as active in GroupQueue with file-watching (no ChildProcess needed)
6. Mark as `idleWaiting` if output dir is empty after drain

After reclaim, new messages flow via IPC input as normal. Containers never
notice the gateway restarted.

### `GroupQueue` changes

`registerProcess` gains an optional `containerName`-only path: the
ChildProcess is optional when a container is reclaimed. `signalContainer`
already uses `docker kill --signal=SIGUSR1 <name>` — no ChildProcess needed.

`state.process` becomes `state.process: ChildProcess | null` with the
distinction that `null` is valid for reclaimed containers. Timeout enforcement
uses `docker kill <name>` directly.

## What stays the same

- IPC input: `ipc/input/*.json` + SIGUSR1 (no change)
- `_close` sentinel and `closeStdin()` (no change)
- Container mounts and directory layout (no change)
- `buildContainerArgs`: stdin still used for initial `ContainerInput` delivery
  (secrets, prompt) — only stdout is replaced
- Timeout enforcement: `docker stop <name>` fallback to `docker kill`
- Session tracking (`recordSessionStart` / `updateSessionEnd`)

## What we lose

- Container startup stderr in real-time. Stderr stays on docker's pipe
  (or is redirected to `logs/container-<ts>.log` via the container
  entrypoint). Loss of live stderr is acceptable — it's debug-only today.

## What we gain

- Gateway restart is non-destructive. In-flight agent responses survive.
- Idle container reclaim is trivial — no re-spawn needed.
- Output stall detection: if no output file appears within N minutes,
  the container is stuck (no ambiguity about whether stdout pipe drained).
- IPC dir is the single communication channel. Simpler mental model.

## Migration

1. `agent-runner/src/index.ts`: change `writeOutput` to write files,
   add `gateway.pid` reader, keep stdout markers for scenario/test mode
2. `container-runner.ts`: add `<ipc-dir>/output/` watcher, pass to
   `onOutput` callback chain; remove stdout `parseBuffer` logic
3. `group-queue.ts`: make `state.process` optional; add reclaim path
   called from `index.ts` startup
4. `index.ts`: add startup reclaim call after orphan scan

## Open questions

- **gateway.pid vs inotify**: gateway.pid is simpler and avoids inotify
  limitations (docker bind mounts, Linux inotify on overlayfs). Preferred.
  Alternative: container just writes files; gateway uses `fs.watch` +
  `setInterval` poll without any signaling — works but adds up to 500ms
  output latency.
- **initial stdin**: secrets are currently passed via stdin to avoid writing
  them to disk. Keep this — close stdin after writing, container reads it
  once on startup. No change needed.
- **output file retention**: delete after processing (current plan) or
  keep for a TTL for replay? Delete is simpler; replay not needed since
  the gateway drains on restart.
- **scenario mode**: `NANOCLAW_SCENARIO` test containers still write to
  stdout for simplicity — gateway can detect scenario mode and skip file
  watching. Or just make scenario mode also write files (preferred for
  full coverage).
