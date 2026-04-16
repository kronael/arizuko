# 015 — MCP IPC

File-based IPC (`messages/`, `tasks/`, `requests/`, `replies/`) replaced
by a Go MCP server over unix socket. Tool names and semantics unchanged.

- Server: `socat STDIO UNIX-CONNECT:/workspace/ipc/gated.sock`
- Socket created per-container-run

Update any skill that references the old IPC file paths.
