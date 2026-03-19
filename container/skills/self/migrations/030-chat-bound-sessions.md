# Chat-bound sessions

The gateway now uses file-based IPC instead of stdin for all agent
communication. Your container reads `start.json` on startup and polls
`input/` for follow-up messages. File deletion is acknowledgment.

## What changed

- **start.json**: written by gateway before spawn, contains session config,
  prompt, secrets. Deleted by agent after reading (contains secrets).
- **input/ directory**: gateway writes message files here for follow-up
  messages during an active session. Agent deletes after processing.
- **No more stdin**: all I/O goes through IPC files.
- **send_reply action**: replies to the current bound conversation using
  the chatJid from the active session. Use `send_reply` for simple replies,
  `send_message` only for cross-chat messaging.
- **IDLE_TIMEOUT removed**: containers exit when input/ is empty and a
  `_close` sentinel is written, not after a timeout.

## Check

No action needed -- this is an informational migration. The agent-runner
already handles the new protocol.

## After

Update MIGRATION_VERSION to 30.
