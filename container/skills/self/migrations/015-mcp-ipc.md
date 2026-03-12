# Migration 015: MCP IPC

File-based IPC (ipc/watcher.go) replaced by Go MCP server over
unix socket. The nanoclaw MCP tools are now served directly by
the router process.

## What changed

- `nanoclaw` MCP server is now `socat STDIO UNIX-CONNECT:/workspace/ipc/router.sock`
- IPC subdirs `messages/`, `tasks/`, `requests/`, `replies/` removed
- `router.sock` created per-container-run in `/workspace/ipc/`
- All actions (send_message, schedule_task, etc.) available as before

## Agent impact

No change to tool names or semantics. If your CLAUDE.md or skills
reference the old IPC file paths, update them to use MCP tools directly.
