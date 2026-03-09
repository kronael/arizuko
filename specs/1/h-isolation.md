# MCP Tool Isolation

MCP servers run inside the agent container today. This spec
describes running each MCP server in its own docker container
with controlled communication — stronger isolation, independent
resource limits, per-group configuration.

Builds on `extend-agent.md`, `extend-gateway.md`, and the
existing `sidecar/whisper/` pattern.

## Current state

MCP servers run inside the agent container:

- **nanoclaw**: built-in, stdio, IPC proxy to gateway
- **agent-registered**: binaries written by agent, launched by
  agent-runner, listed in `~/.claude/settings.json`

Whisper sidecar (`sidecar/whisper/`) is a partial precedent:
separate docker image, HTTP API on port 8178. The agent calls
it via an MCP tool in nanoclaw. Gateway does not manage its
lifecycle — whisper is started externally and runs persistently.

This spec extends that pattern: gateway-managed sidecars, unix
socket transport, per-group configuration.

## Architecture

```
Gateway
  ├── Agent container (nanoclaw-<group>-<ts>)
  │     /workspace/ipc/sidecars/<name>.sock  ← socket mount
  │     ~/.claude/settings.json              ← sidecar MCP entries
  └── Sidecar containers (per MCP server)
        nanoclaw-sidecar-<name>-<group>
        /run/socks/<name>.sock               ← same socket, other side
```

Each sidecar exposes a single MCP server over a unix socket.
The agent connects via `settings.json` using the `mcp-server-sdk`
socket transport. No HTTP ports, no port allocation.

## Socket paths

Each sidecar gets its own unix socket, named by sidecar name:

```
data/sessions/<group>/.claude/sidecars/<name>.sock
```

Host path translated through `hostPath()` as with other mounts.
Inside the agent container, all sidecar sockets appear under:

```
/workspace/ipc/sidecars/<name>.sock
```

Inside each sidecar container:

```
/run/socks/<name>.sock
```

Both containers mount the same host socket directory. Each sidecar
binds on `/run/socks/<name>.sock` (via `$MCP_SOCK` env); the
agent connects to `/workspace/ipc/sidecars/<name>.sock`.

## Sidecar configuration

Sidecars are configured per-group in `containerConfig.sidecars`
(stored as JSON in `registered_groups.container_config`):

```typescript
interface SidecarSpec {
  image: string; // docker image
  env?: Record<string, string>;
  // docker resource limits
  memoryMb?: number; // --memory
  cpus?: number; // --cpus
  network?: 'bridge' | 'none'; // default: none (→ see isolation modes)
  mode?: 'privileged' | 'offline' | 'web'; // default: offline
  // which MCP tools to expose to the agent (allowlist)
  allowedTools?: string[]; // ["search", "fetch"] or ["*"]
}

interface ContainerConfig {
  timeout?: number;
  additionalMounts?: VolumeMount[];
  sidecars?: Record<string, SidecarSpec>;
}
```

Global defaults can also be set in `.env`:

```bash
# Enable a sidecar for all groups (image only, no per-group config)
SIDECAR_WEBSEARCH_IMAGE=nanoclaw-sidecar-websearch:latest
SIDECAR_CODEEXEC_IMAGE=nanoclaw-sidecar-codeexec:latest
```

Per-group config in `registered_groups` takes precedence.

## Sidecar lifecycle

Gateway manages sidecar containers alongside the agent container.
Lifecycle hooks in `runContainerAgent()`:

### 1. Start (before agent)

```typescript
async function startSidecars(
  group: RegisteredGroup,
  sockDir: string,
): Promise<SidecarHandle[]> {
  const specs = resolveSidecars(group); // env + containerConfig merge
  return Promise.all(specs.map((s) => startSidecar(s, group, sockDir)));
}

async function startSidecar(
  spec: SidecarSpec,
  group: RegisteredGroup,
  sockDir: string,
): Promise<SidecarHandle> {
  const name = `nanoclaw-sidecar-${spec.name}-${safeName(group.folder)}`;
  const sockPath = path.join(sockDir, `${spec.name}.sock`);
  const args = [
    'run',
    '-d',
    '--rm',
    '--name',
    name,
    `--memory=${spec.memoryMb ?? 256}m`,
    `--cpus=${spec.cpus ?? 0.5}`,
    `--network=${spec.network ?? 'none'}`,
    '-v',
    `${hostPath(sockDir)}:/run/socks`,
    '-e',
    `MCP_SOCK=/run/socks/${spec.name}.sock`,
    ...envArgs(spec.env),
    spec.image,
  ];
  await exec(`${CONTAINER_RUNTIME_BIN} ${args.join(' ')}`);
  await waitForSocket(sockPath, { timeoutMs: 5000 });
  return {
    containerName: name,
    specName: spec.name,
    sockPath,
    allowedTools: spec.allowedTools,
  };
}
```

`waitForSocket()` polls until the unix socket appears (sidecar
ready) or times out. On timeout, log and skip — agent proceeds
without that sidecar.

### 2. Health check

After socket appears, gateway checks connectivity:

```typescript
async function probeSidecar(sockPath: string): Promise<boolean> {
  // Connect, send MCP initialize, check response
  // Returns false on timeout or protocol error
}
```

Failed probe: sidecar excluded from agent's `settings.json`,
warning logged. Agent continues without it.

### 3. Inject into agent settings.json

Before agent container starts, gateway appends sidecar MCP
entries to the group's `settings.json`:

```typescript
const sidecarMcp: Record<string, McpServerEntry> = {};
for (const h of handles) {
  sidecarMcp[h.name] = {
    command: 'socat',
    args: [`UNIX-CONNECT:/workspace/ipc/sidecars/${h.name}.sock`, 'STDIO'],
  };
}
// Merge: agent-written servers < sidecars < nanoclaw (nanoclaw wins)
settings.mcpServers = {
  ...settings.mcpServers,
  ...sidecarMcp,
  nanoclaw: { ... },
};
```

`socat` is available in the agent container. It bridges stdio
(what the MCP SDK expects) to the unix socket.

### 4. Stop (after agent exits)

```typescript
async function stopSidecars(handles: SidecarHandle[]): Promise<void> {
  await Promise.all(
    handles.map((h) => exec(stopContainer(h.name)).catch(() => {})),
  );
}
```

Called in `container.on('close', ...)` after the agent container
exits. Fire-and-forget with swallowed errors — sidecar cleanup
is best-effort.

## Permission model

Two layers:

**1. Tool allowlist per sidecar** (`allowedTools` in `SidecarSpec`):
Gateway adds only allowed tools to `allowedTools` in `settings.json`.
Wildcards supported: `["*"]` passes all tools; `["search"]` passes
only `mcp__<name>__search`.

```typescript
const mcpWildcards = handles.flatMap((h) => {
  if (!h.allowedTools || h.allowedTools.includes('*'))
    return [`mcp__${h.name}__*`];
  return h.allowedTools.map((t) => `mcp__${h.name}__${t}`);
});
```

**2. Group authorization** (`containerConfig.sidecars` presence):
A group only gets a sidecar if it has a `SidecarSpec` for it —
either in its `containerConfig` or via `SIDECAR_<NAME>_IMAGE` env
with no group-level override that disables it.

Root groups can self-configure sidecars via action; non-root groups
inherit gateway-configured defaults.

## Resource limits

Per-sidecar in `SidecarSpec`:

| Field      | Docker flag | Default |
| ---------- | ----------- | ------- |
| `memoryMb` | `--memory`  | 256m    |
| `cpus`     | `--cpus`    | 0.5     |
| `network`  | `--network` | `none`  |

No PIDs limit by default (most MCP servers are single-process).

## Examples

### a. Web-search sidecar

Network access, no filesystem writes.

```json
{
  "sidecars": {
    "websearch": {
      "image": "nanoclaw-sidecar-websearch:latest",
      "network": "bridge",
      "memoryMb": 256,
      "cpus": 0.25,
      "allowedTools": ["search", "fetch"],
      "env": { "SEARCH_API_KEY": "..." }
    }
  }
}
```

Image entrypoint: MCP server over `$MCP_SOCK`. No filesystem
access — image has no mounts, `--network=bridge` for outbound HTTP.

### b. Code-execution sidecar

Filesystem access, no network.

```json
{
  "sidecars": {
    "codeexec": {
      "image": "nanoclaw-sidecar-codeexec:latest",
      "network": "none",
      "memoryMb": 512,
      "cpus": 1.0,
      "allowedTools": ["run_code", "read_file", "write_file"]
    }
  }
}
```

Gateway adds a scratch mount:

```typescript
// In startSidecar, when additionalMounts present in SidecarSpec:
args.push('-v', `${scratchDir}:/workspace`);
```

Agent can pass file paths to `run_code`; sidecar reads/writes
in `/workspace` (mapped to an isolated temp dir on host).

### c. Per-group config (three ways)

**Via `.env` (all groups, no filtering):**

```bash
SIDECAR_WEBSEARCH_IMAGE=nanoclaw-sidecar-websearch:latest
```

**Via `nanoclaw config` CLI (single group):**

```bash
nanoclaw config REDACTED group sidecars set main \
  '{"websearch":{"image":"nanoclaw-sidecar-websearch:latest","network":"bridge"}}'
```

Stored in `registered_groups.container_config` as JSON.

**Via agent action (root group self-configures):**

Agent calls `configure_sidecar` action (gateway-side) which
validates and persists to `registered_groups`. Takes effect on
next spawn.

## Relation to whisper sidecar

Whisper (`sidecar/whisper/`) uses HTTP on port 8178 and is
started externally. It predates this spec. Migration path:

1. Add MCP server wrapper to whisper image (expose over
   `$MCP_SOCK` instead of HTTP)
2. Add `SIDECAR_WHISPER_IMAGE` env support to gateway
3. Remove port 8178 nanoclaw tool, replace with
   `mcp__whisper__transcribe`
4. Whisper container started/stopped per-agent, not persistent

Until migration: whisper continues to run as HTTP sidecar.
New sidecars follow this spec.

## Gateway code changes

| File                  | Change                                                   |
| --------------------- | -------------------------------------------------------- |
| `container-runner.ts` | `startSidecars()`, `stopSidecars()` around agent spawn   |
| `container-runner.ts` | Socket dir created in `buildVolumeMounts()`              |
| `container-runner.ts` | `settings.json` injection for sidecar MCP                |
| `config.ts`           | `SIDECAR_*_IMAGE` env vars                               |
| `types.ts`            | `SidecarSpec`, `SidecarHandle` types                     |
| `db.ts`               | No schema change — `container_config` JSON column exists |

## Isolation modes

Same three modes as agent-driven sidecars (`mcp-sidecar.md`):

| Mode           | Files | Network | IPC | Example                    |
| -------------- | ----- | ------- | --- | -------------------------- |
| **privileged** | yes   | yes     | yes | full-access trusted tool   |
| **offline**    | yes   | no      | no  | code exec, file processing |
| **web**        | no    | yes     | no  | search, API fetching       |

Gateway-configured sidecars set mode in `SidecarSpec`:

```typescript
interface SidecarSpec {
  // ... existing fields ...
  mode?: 'privileged' | 'offline' | 'web'; // default: offline
}
```

Gateway translates mode to docker flags (network, mounts).
See `mcp-sidecar.md` for flag mapping.

> **Status**: to spec and resolve. Needs validation against
> real sidecar use cases before implementation.

## Future: Firecracker / gVisor

Unix sockets work for docker. For stronger isolation:

- **gVisor (runsc)**: drop-in docker runtime (`--runtime=runsc`).
  Unix sockets still work. Lower overhead than VMs.
- **Firecracker**: virtio-vsock replaces unix sockets.
  `socat VSOCK-CONNECT:<cid>:7000 STDIO` in agent container.
  `socat VSOCK-LISTEN:7000 EXEC:./mcp-server` in microVM.
  Gateway assigns vsock CIDs per sidecar.

Transport abstraction (`UnixSocketTransport` vs `VsockTransport`)
keeps sidecar images unchanged — only the gateway launch args
and the agent's `settings.json` socat command change.

## Open questions

- **socat availability**: verify socat is in agent image; add to
  Dockerfile if missing.
- **Socket cleanup**: stale `.sock` files from crashed sidecars.
  Gateway should unlink before bind. Or use abstract sockets.
- **Persistent sidecars**: some (e.g. model-loaded whisper) are
  expensive to start per-agent. Pool them? Named persistent
  containers reused across spawns. Lifecycle becomes gateway-scoped,
  not agent-scoped.
- **Sidecar image registry**: where do sidecar images live? Build
  convention (nanoclaw-sidecar-<name>) vs explicit image URIs.
- **Secret injection**: sidecars need API keys. Options: env in
  `SidecarSpec` (stored in DB — avoid), gateway-managed secrets
  file, or `.env` interpolation at start time.
- **Sidecar-to-gateway IPC**: sidecars cannot call gateway actions
  today. If needed, mount gateway unix socket into sidecar too.
- **Mount allowlist UX**: `~/.config/nanoclaw/mount-allowlist.json`
  is a separate file from the DB, requires manual JSON editing,
  and lives at a hardcoded path outside the project. Problems:
  (1) not discoverable — no CLI for managing it,
  (2) not per-instance — one allowlist for all instances,
  (3) format is verbose (AllowedRoot objects with description).
  Consider: move to per-instance `.env` var (`MOUNT_ROOTS=/srv/data`),
  or store in DB alongside container_config, or at minimum add a
  `nanoclaw config <inst> mount allow <path>` CLI command.
