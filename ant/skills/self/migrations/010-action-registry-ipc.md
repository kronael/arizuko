# 010 — Action registry and request-response IPC

MCP tools now use request-response IPC instead of fire-and-forget.
Agent writes to `/workspace/ipc/requests/`, polls `/workspace/ipc/replies/`.
Gateway dispatches through action registry and writes typed replies.

The `action_manifest.json` file in `/workspace/ipc/` lists all
available actions with their schemas.

Fire-and-forget IPC (`messages/`, `tasks/` dirs) still works as
fallback for older agent images.
