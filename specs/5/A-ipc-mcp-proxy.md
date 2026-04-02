---
status: draft
---

<!-- trimmed 2026-03-15: shipped/archived -->

# IPC -> MCP Proxy — SHIPPED

Replaced hand-rolled IPC file dispatch with MCP over unix socket.
Transport: stdio over socat to per-group socket (`ipc/<folder>/gated.sock`).
Auth: unix socket filesystem permissions (0600). nanoclaw replaced by
Go MCP server (`ipc`). IPC file watcher deleted -- hard cutover complete.
