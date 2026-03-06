# IPC -> MCP Proxy (v2)

## Problem

Current IPC is a hand-rolled MCP proxy: agent -> nanoclaw
MCP server -> IPC files -> fs.watch -> dispatch. Reinvents
MCP with files as transport.

## Proposed architecture

```
Agent (SDK) -> MCP client -> gateway MCP server
  (stdio pipe or unix socket) -> action registry
  -> channels, DB, scheduler
```

No IPC files, no fs.watch, no manual JSON serialization.

## Permission model

Container boundary = security. Action registry IS the
permission layer: `ctx.isRoot`, `ctx.sourceGroup`, typed
schemas, explicit action set.

## What changes

| Component    | v1                       | v2                       |
| ------------ | ------------------------ | ------------------------ |
| Transport    | IPC files + fs.watch     | MCP over stdio/socket    |
| In-container | nanoclaw (full MCP)      | thin proxy or direct     |
| Gateway      | manual type switch       | MCP handler per action   |
| Validation   | ad-hoc in ipc.ts         | Zod from action registry |
| Latency      | file write + watch delay | direct RPC               |

## Three MCP layers

- **Gateway MCP** -- actions (send_message, schedule_task)
- **Sidecar MCP** -- extensions (whisper, etc.), proxy
  multiplexes
- **Agent MCP** -- agent's own tools, container-local

nanoclaw may become unnecessary if gateway MCP replaces it.

## Migration path

1. v1: action registry with file IPC (actions.md)
2. v2: gateway MCP server over per-group unix socket
3. Agent-runner connects via socat shim
4. Deprecate nanoclaw action-proxy tools
5. Remove IPC file dispatch

## Per-group socket

```
data/sessions/main/ipc/gateway.sock  -> group: main
```

Container: `/workspace/ipc/gateway.sock`. Gateway knows
group by which socket received the connection.

## Open questions

- **Transport**: unix socket for docker, virtio-vsock for
  Firecracker/QEMU. socat bridges both.
- **Bidirectional**: gateway push via MCP notifications?
  Would replace SIGUSR1 + input file polling.
- **nanoclaw fate**: proxy client or replaced by direct
  SDK connection?
- **Middleware**: pre/post hooks on MCP calls (logging,
  rate limiting, transforms).
