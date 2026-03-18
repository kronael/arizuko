# ipc

**Status**: shipped — `ipc/` package (ipc.go, all 16 tools, runtime auth via auth)

MCP daemon. Per-group MCP server on unix socket. Resolves caller
identity from socket path, authorizes via auth, executes all
16 tools inline via handler functions.

## Role

ipc is the single MCP entry point for all agent containers.
It does three things:

1. **Accept** MCP connections from agent containers
2. **Stamp** each request with caller identity (folder, tier)
3. **Execute** the tool inline (handlers call gateway/store/timed functions directly)

No forwarding to other daemons. All tool logic runs inside
ipc's handlers, which receive gateway callbacks at setup time.

## Identity resolution

Each agent container connects from a known group folder.
ipc resolves identity from the socket path:

```
/data/ipc/<folder>/nanoclaw.sock → folder = <folder>
```

Tier is computed from folder depth (slash count):

| Depth | Tier | Name   | Example folder     |
| ----- | ---- | ------ | ------------------ |
| 0     | 0    | root   | `andy`             |
| 1     | 1    | world  | `andy/research`    |
| 2     | 2    | agent  | `andy/research/qa` |
| 3+    | 3    | worker | `andy/r/qa/sub`    |

## MCP server

One unix socket per group. Agent containers connect via
socat bridge:

```json
{
  "nanoclaw": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/nanoclaw.sock"]
  }
}
```

ipc exposes all 16 tools in a single tool list. Authorization
is checked per-call via auth.Authorize before execution.

## Tools

All tools are handled inline by ipc. Gateway and store
functions are injected as callbacks at server creation time.

| Tool             | Domain     |
| ---------------- | ---------- |
| `send_message`   | messaging  |
| `send_file`      | messaging  |
| `register_group` | groups     |
| `reset_session`  | sessions   |
| `delegate_group` | groups     |
| `inject_message` | messaging  |
| `escalate_group` | groups     |
| `get_routes`     | routing    |
| `set_routes`     | routing    |
| `add_route`      | routing    |
| `delete_route`   | routing    |
| `schedule_task`  | scheduling |
| `list_tasks`     | scheduling |
| `pause_task`     | scheduling |
| `resume_task`    | scheduling |
| `cancel_task`    | scheduling |

## Request flow

```
agent calls send_message("hello")
  → ipc receives on /data/ipc/andy/research/nanoclaw.sock
  → resolves: folder=andy/research, tier=1
  → calls auth.Authorize: can tier=1 from andy/research do send_message?
  → auth: allow (tier 1 ≤ min tier 3)
  → ipc executes send_message via gateway callback
  → result returned to agent
```

## No tables owned

ipc is stateless. It doesn't own any database tables.
It reads group information from the filesystem (socket paths)
and computes tier from folder depth. No migrations.

## Layout

```
ipc/
  ipc.go
```
