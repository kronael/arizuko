# 010 — Action registry and request-response IPC

**SUPERSEDED by migration 015.** File-based `requests/`/`replies/` IPC
was removed when the gateway ported from TypeScript to Go. MCP over unix
socket (`/workspace/ipc/gated.sock`) is the only IPC transport. See
migration 015 for current behavior.
