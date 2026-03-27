---
status: draft
---

# ipc

**Status**: shipped — `ipc/` package (ipc.go, all 20 tools, runtime auth via auth)

MCP daemon. Per-group MCP server on unix socket. Resolves caller
identity from socket path, authorizes via auth, executes all 20
tools inline via handler functions.

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
/data/ipc/<folder>/router.sock → folder = <folder>
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
  "arizuko": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/workspace/ipc/router.sock"]
  }
}
```

ipc exposes all 20 tools in a single tool list. Authorization
is checked per-call via auth.Authorize before execution.

## Tools

All tools are handled inline by ipc. Gateway and store
functions are injected as callbacks at server creation time.

| Tool             | Domain        | Gating                    |
| ---------------- | ------------- | ------------------------- |
| `send_message`   | messaging     | grants                    |
| `send_reply`     | messaging     | grants                    |
| `send_file`      | messaging     | grants                    |
| `inject_message` | messaging     | grants                    |
| `register_group` | groups        | grants + auth.Authorize   |
| `delegate_group` | groups        | grants + auth.Authorize   |
| `escalate_group` | groups        | grants                    |
| `refresh_groups` | groups        | tier ≤ 2                  |
| `reset_session`  | sessions      | grants + auth.Authorize   |
| `get_routes`     | routing       | grants + auth.Authorize   |
| `set_routes`     | routing       | grants + auth.Authorize   |
| `add_route`      | routing       | grants + auth.Authorize   |
| `delete_route`   | routing       | grants + auth.Authorize   |
| `schedule_task`  | scheduling    | grants + auth.Authorize   |
| `list_tasks`     | scheduling    | grants                    |
| `pause_task`     | scheduling    | grants + auth.Authorize   |
| `resume_task`    | scheduling    | grants + auth.Authorize   |
| `cancel_task`    | scheduling    | grants + auth.Authorize   |
| `get_grants`     | authorization | tier ≤ 1 + auth.Authorize |
| `set_grants`     | authorization | tier ≤ 1 + auth.Authorize |

`register_group`: `name` is optional (defaults to jid). When `fromPrototype=true`,
copies the caller's `prototype/` directory into the new child folder before
registering. Merges the former `spawn_group` tool.

## Request flow

```
agent calls send_message("hello")
  → ipc receives on /data/ipc/andy/research/router.sock
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
