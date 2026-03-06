# IPC → MCP Proxy (v2)

## Problem

The current IPC is a hand-rolled MCP proxy:

```
Agent → nanoclaw MCP server (in-container) → writes IPC files
  → gateway fs.watch → dispatches action by `type` field
```

This reinvents MCP with files as transport. The agent already speaks
MCP. The gateway already dispatches typed actions. The file layer is
unnecessary ceremony.

## Observation

The gateway is already an MCP server in disguise. The action registry
(actions.md) defines typed operations with Zod schemas, authorization
checks, and handlers. These map 1:1 to MCP tools.

## Proposed architecture

```
Agent (Claude Code SDK)
  → MCP client (built into SDK)
    → gateway MCP server (over stdio pipe or unix socket)
      → action registry (permission checks, dispatch)
        → channels, DB, task scheduler
```

No IPC files. No fs.watch. No manual JSON serialization. The SDK's
MCP client talks directly to the gateway's MCP server over the
container's stdio pipe (already connected) or a mounted unix socket.

## Why not full access?

The container boundary exists for security. The agent is untrusted
LLM-driven code. The action registry IS the permission layer:

- `ctx.isRoot` — root group can target any JID
- `ctx.sourceGroup` — non-root can only act on own group
- Typed schemas — reject malformed input
- Explicit action set — agent can only do what's registered

Direct DB/channel/config access would break containment. The MCP
proxy preserves the same permission boundary, just with a clean
transport.

## What changes

| Component           | Current (v1)                    | Proposed (v2)                           |
| ------------------- | ------------------------------- | --------------------------------------- |
| Transport           | IPC files + fs.watch            | MCP over stdio/socket                   |
| In-container server | nanoclaw (full MCP impl)        | thin MCP proxy or direct SDK connection |
| Gateway dispatch    | manual `type` switch in ipc.ts  | MCP server handler per action           |
| Schema validation   | ad-hoc in ipc.ts                | Zod schemas from action registry        |
| Latency             | write file + watch delay + poll | direct RPC                              |

## Agent-registered MCP servers

Agent self-extension (extend-agent.md) already lets agents register
MCP servers via settings.json. These are local to the container. The
gateway MCP proxy is separate — it's the agent's interface TO the
gateway, not the agent extending itself.

Three MCP layers coexist:

- **Gateway MCP server** → actions (send_message, schedule_task, etc.)
  These are gateway-internal, always tied to gateway components
  (channels, DB, scheduler). No external action extensions needed.
- **Sidecar MCP servers** → gateway-side extensions (whisper, etc.)
  The proxy can multiplex these — agent calls `mcp__whisper__transcribe`,
  proxy routes to the sidecar process. Agent doesn't know or care
  whether it's gateway-native or a sidecar.
- **Agent MCP servers** → agent's own tools (media processing, custom
  scripts). Local to the container, registered via settings.json.

nanoclaw → may become unnecessary if gateway MCP server replaces it.

## Migration path

1. v1: action registry with file-based IPC (actions.md — in progress)
2. v2: add gateway MCP server over per-group unix socket
3. Agent-runner connects to gateway MCP server via socat shim
4. Deprecate nanoclaw's action-proxy tools (send_message etc.)
5. Remove IPC file dispatch when all actions migrate

## Per-group socket identification

One socket per group — gateway creates it in the group's IPC dir:

```
/srv/data/kanipi_X/data/sessions/main/ipc/gateway.sock  → group: main
/srv/data/kanipi_X/data/sessions/ops/ipc/gateway.sock   → group: ops
```

Container sees it at `/workspace/ipc/gateway.sock`. Gateway knows
which group is talking by which socket received the connection.
No auth handshake needed — same model as file-based IPC.

## Open questions

- **Transport**: unix socket (`/workspace/ipc/gateway.sock`) for docker.
  Unix sockets don't cross VM boundaries — Firecracker/QEMU need
  virtio-vsock (`AF_VSOCK`, CID:port). socat bridges both:
  docker = `socat UNIX:gateway.sock -`,
  firecracker = `socat VSOCK-CONNECT:2:5000 -`.
  Agent-runner config is the same, only socat address changes.
- **Bidirectional**: can the gateway push to the agent via MCP
  notifications? Would replace SIGUSR1 + input file polling.
- **nanoclaw fate**: does it become the gateway MCP proxy client,
  or does the SDK connect to the gateway MCP server directly?
- **Hook points along the MCP call**: how do extensions register
  pre/post hooks on MCP tool calls? e.g. logging, rate limiting,
  transforming input/output, injecting context. Need a middleware
  chain on the gateway MCP server — each action invocation passes
  through registered hooks before/after the handler runs.
