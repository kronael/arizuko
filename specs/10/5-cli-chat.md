---
status: unshipped
depends: [29-local-cli]
---

# CLI Chat

`arizuko chat <instance>` launches Claude Code with MCP access to the
instance's root IPC socket. The operator gets an interactive Claude
session that can use all arizuko MCP tools — send messages, manage
groups, delegate, view history, etc.

This is not a custom TUI. It launches `claude` (Claude Code CLI) with
the right MCP server configured. Claude Code is the interface.

## Interface

```bash
arizuko chat <instance>
```

Resolves data dir via `instanceDir(instance)` (same as other commands),
finds the root group's IPC socket, launches Claude Code with the
arizuko MCP server attached.

## MCP wiring

The IPC socket at `<dataDir>/ipc/main/gated.sock` is already a full
MCP server (stdio over unix socket). The agent container connects to
it via socat:

```
socat STDIO UNIX-CONNECT:/workspace/ipc/gated.sock
```

CLI chat uses the same transport. The `chat` command:

1. Resolves `dataDir` = `instanceDir(instance)`
2. Validates socket exists at `<dataDir>/ipc/main/gated.sock`
3. Writes a temp MCP config JSON:
   ```json
   {
     "mcpServers": {
       "arizuko": {
         "command": "socat",
         "args": ["STDIO", "UNIX-CONNECT:<dataDir>/ipc/main/gated.sock"]
       }
     }
   }
   ```
4. Execs `claude --mcp-config <tmpfile>`
5. Temp file is cleaned up on exit (defer)

If Claude Code CLI supports `--mcp-config` as a flag, use it directly.
Otherwise, write to `~/.claude/settings.local.json` or a project-level
`.claude/settings.local.json` — check CLI docs at implementation time.

## Auth model

No additional auth. The operator runs `arizuko chat` on the host where
the data dir lives. Socket access = root. The socket serves tier-0
identity (folder="main"), which has unrestricted MCP tool access.

The root socket grants `**` (all tools, all targets). Same privileges
as the root agent container.

## Root folder detection

The default root group is `main` (created by `arizuko create`). To
support non-default root folders:

1. Open `<dataDir>/store/messages.db`
2. Query for the tier-0 group (no `/` in folder name, first registered)
3. Use its folder for the socket path

For v1, hardcode `main`. The `create` command always creates `main`
as the default group.

## Available tools

All tools from `ipc.buildMCPServer` at tier 0:

- `send`, `reply`, `send_file` — message any chat
- `inject_message` — write directly to the message store
- `register_group`, `delegate_group`, `escalate_group` — group management
- `reset_session` — clear agent sessions
- `schedule_task`, `pause_task`, `resume_task`, `cancel_task`, `list_tasks`
- `list_routes`, `set_routes`, `add_route`, `delete_route`
- `get_grants`, `set_grants` — ACL management
- `get_history` — read message history
- `set_web_host`, `get_web_host` — vhost management
- `refresh_groups` — reload group list

## Implementation

Add `cmdChat` to `cmd/arizuko/main.go`:

```go
case "chat":
    cmdChat(os.Args[2:])
```

The function:

- Validates instance argument
- Checks socket exists (fail with helpful message if gated isn't running)
- Checks `claude` is in PATH
- Writes temp config, execs claude, defers cleanup

~30 lines of Go. No new packages.

## Prerequisites

- `claude` CLI installed on operator's machine
- `socat` installed on operator's machine
- Instance running (gated must be up, socket must exist)
- Operator has filesystem access to the data dir

## Out of scope

- Custom TUI — Claude Code provides the interface
- Auth beyond socket access — local operator is trusted
- History persistence beyond Claude Code's built-in sessions
- Multi-user — one operator at a time per socket
- Remote access — socket is local; use adapters for remote
- Streaming/SSE — Claude Code handles its own streaming
- Custom system prompt — Claude Code uses its own; arizuko
  tools are self-describing via MCP
