# MCP Sidecars

MCP servers in isolated docker containers. Two provisioning
modes: gateway-managed (operator config) and agent-requested
(IPC action at runtime).

**Status**: gateway-managed pattern exists (whisper sidecar).
Agent-requested not started.

## Current state

- **nanoclaw**: built-in MCP, stdio, IPC proxy to gateway
- **agent-registered**: agent writes settings.json, runs
  inside agent container (no isolation)
- **whisper**: separate docker image, HTTP on port 8178,
  externally managed (partial precedent)

## Architecture

```
Gateway
  ├── Agent container
  │     /workspace/ipc/sidecars/<name>.sock  ← socket mount
  │     ~/.claude/settings.json              ← sidecar entries
  └── Sidecar containers (per MCP server)
        /run/socks/<name>.sock               ← same socket
```

Unix socket transport. No HTTP ports, no port allocation.

## Socket paths

```
Host:    data/sessions/<group>/.claude/sidecars/<name>.sock
Agent:   /workspace/ipc/sidecars/<name>.sock
Sidecar: /run/socks/<name>.sock
```

Both containers mount the same host socket directory.

## Gateway-managed sidecars

Operator configures per-group in `container_config.sidecars`
or globally via env. Started before agent, stopped after.

```typescript
interface SidecarSpec {
  image: string;
  env?: Record<string, string>;
  memoryMb?: number; // default: 256
  cpus?: number; // default: 0.5
  network?: 'bridge' | 'none'; // default: none
  mode?: 'privileged' | 'offline' | 'web';
  allowedTools?: string[];
}
```

Global defaults via env:

```bash
SIDECAR_WEBSEARCH_IMAGE=nanoclaw-sidecar-websearch:latest
SIDECAR_CODEEXEC_IMAGE=nanoclaw-sidecar-codeexec:latest
```

Lifecycle: start all → wait for sockets → agent runs → stop all.

## Agent-requested sidecars

Agent calls `request_sidecar` IPC action. Gateway validates
and spawns. Agent connects via socket.

```typescript
// IPC action
request_sidecar: {
  name: string,         // ^[a-z0-9-]+$
  image: string,        // must match allowlist
  command?: string[],
  env?: Record<string, string>,
  memoryMb?: number,    // max: 1024
  cpus?: number,        // max: 2.0
  network?: 'none' | 'bridge',
  mode?: 'privileged' | 'offline' | 'web',
  mounts?: { src: string, dst: string, ro?: boolean }[],
}

// Reply
{ ok: true, name: string, sockPath: string }
```

Also: `stop_sidecar { name }`, `list_sidecars {}`.

### Validation

- **Image allowlist**: `SIDECAR_ALLOWED_IMAGES=node:22-slim,python:3.12-slim,nanoclaw-sidecar-*`
- **Mount restrictions**: relative paths only, under /workspace/group/, default ro
- **Resource caps**: memoryMb max 1024, cpus max 2.0, network requires allowlist
- **Max per group**: `MAX_SIDECARS_PER_GROUP=4`

### Lifecycle

1. Agent calls request_sidecar
2. Gateway validates, spawns, waits for socket
3. Returns socket path
4. Agent connects via socat or native socket
5. On agent exit: gateway stops all agent-requested sidecars

## Isolation modes

| Mode           | Files | Network | IPC | Use case             |
| -------------- | ----- | ------- | --- | -------------------- |
| **privileged** | yes   | yes     | yes | full access, trusted |
| **offline**    | yes   | no      | no  | code exec, file proc |
| **web**        | no    | yes     | no  | search, API calls    |

Default: offline (safest). Privileged requires operator
allowlist (`SIDECAR_PRIVILEGED_IMAGES`).

## Key files

- `src/container-runner.ts` — sidecar start/stop alongside agent
- `src/actions/sidecars.ts` — NEW: request/stop/list actions
- `src/config.ts` — allowlist env vars

## Open questions

1. **Startup latency** — IPC + docker run + socket wait = 5-10s.
   Hot sidecar pooling could reduce to <1s.
2. **Persistence** — agent-requested sidecars die with agent.
   Gateway-managed persist. `persistent: true` flag?
3. **Sidecar-to-gateway IPC** — can sidecar call gateway
   actions? Needs own auth token if so.
4. **Image pull** — agent requests unavailable image. Pre-pull
   at startup, or fail fast?
