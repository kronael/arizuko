---
status: shipped
---

# Detached containers

Collapse the two ant→gated channels (MCP unix socket + stdout markers)
onto one. New JSON-RPC method `submit_turn` over the existing MCP
socket; ant calls it once per turn after the SDK loop completes. Gated
persists in `messages` and via a tiny `turn_results` idempotency table
keyed on `(folder, turn_id)`.

`submit_turn` is hidden from `tools/list` — registered as a separate
JSON-RPC method on the connection layer so the LLM never sees it.

`turn_id` is the originating inbound `messages.id` for the turn.
Gateway already plumbs `Input.MessageID`; ant carries it through and
includes it in the submit_turn payload.

## Rationale

- Restart safety becomes a property of persistence. The agent records
  `(folder, turn_id, session_id, status)` synchronously over MCP; if
  gated dies after persistence, the row is durable and the duplicate
  call on retry is absorbed by the idempotency key.
- One channel beats two. The marker scanner, fullBuf/parseBuf,
  hadStreaming/idleResets, and all OnOutput plumbing go away.
- No file watcher, no SIGUSR2, no `docker ps` reaper, no WAL table.

## Container lifecycle

- `container/runner.go` spawns docker, writes the input JSON to stdin,
  closes stdin, waits for exit. Stderr is logged. Stdout is no longer
  parsed.
- Final `out.NewSessionID` and result delivery happen via the
  submit_turn callback during the run, not after.
- Agent crash before `submit_turn` → non-zero exit → existing failure
  path takes over.
