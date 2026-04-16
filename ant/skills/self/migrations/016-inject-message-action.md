# 016 — inject_message action

Gateway action `inject_message` inserts a message directly into the DB
without sending to the channel. Useful for programmatic retry after OOM
or admin intervention. Clears the chat's `errored` flag.

Root and world groups only (tier ≤ 1).

(File-based IPC syntax superseded by MCP; call via `inject_message` MCP tool.)
