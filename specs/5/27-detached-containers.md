---
status: unshipped
---

# Detached containers

Today `gated` runs each turn as `docker exec` and reads agent output
from stdout via `---ARIZUKO_OUTPUT_START---`/`END` JSON markers. Two
problems fall out: a `gated` restart kills the exec mid-turn (output
lost), and the agent has two channels to talk to the host (MCP socket
for tools, marker stdout for turn result) when one is enough.

## Plan

One channel: the existing per-folder MCP unix socket. `ant` calls
`submit_turn` over it when the SDK loop completes. `gated` persists
the result in `messages` (already durable in WAL). Container exits
when its turn is done — no detach, no idle reaper. On the next
inbound, `gated` spawns a fresh container.

Restart safety becomes a property of the persistence layer, not the
process layer: a turn is durable iff `submit_turn` returned to `ant`
before `gated` died. Anything earlier is "agent never reported", and
a fresh inbound retriggers from the message row.

## Wire

`submit_turn` is a JSON-RPC method on the MCP socket, excluded from
`tools/list` so the LLM never sees it.

```json
{
  "method": "submit_turn",
  "params": {
    "status": "ok", // ok | error
    "result": "…agent text…",
    "session_id": "abc123", // optional, when SDK rotates it
    "error": "" // when status="error"
  }
}
```

Auth: same SO_PEERCRED as MCP tools. Identity stamped from socket
path. No new auth surface.

## What changes

- `ant/src/index.ts` — replace the four `console.log(MARKER...)`
  calls (lines 174/180) with one `submit_turn` JSON-RPC call on the
  same MCP transport. Stdout markers gone.
- `ipc/` — register `submit_turn` handler. Persist via gateway
  callback. Hide from advertised tool list.
- `container/runner.go` — delete the marker scanner and the
  `OnOutput`/`fullBuf`/`parseBuf` plumbing. `docker exec` becomes a
  one-shot wait for exit code; the meaningful result arrives via the
  socket before the container exits.
- `gateway/` — turn-completion callback writes `messages` row,
  rotates session if `session_id` set.

## Restart

- MCP socket lives at `/srv/data/<inst>/ipc/<folder>/gated.sock`
  on disk. New `gated` `unlink`+`bind`s and re-`accept`s. Existing
  `ant` instances reconnect on read error (already does this via
  socat retry).
- A turn in progress when `gated` dies: `ant` retries `submit_turn`
  on reconnect. Idempotent because `messages` upserts on
  `(folder, turn_id)` (turn_id minted by ant from input message id).
- A container running with no peer: the next inbound message for
  that folder reuses the live socket. If the container has exited,
  spawn a new one. No `docker ps` reconciliation needed.

## What gets deleted

- `OUTPUT_START_MARKER`/`OUTPUT_END_MARKER` constants and parser.
- `fullBuf`, `parseBuf`, `hadStreaming`, `idleResets` in
  `container/runner.go`.
- The streaming progress UI hook (`OnOutput`). If we want progress
  later, add an MCP `progress` notification on the same socket.

## Supersedes

- [D-message-wal.md](D-message-wal.md) — inbound durability becomes
  the same idempotent submit: `ant` reads the message, processes,
  calls `submit_turn` with the originating `turn_id`. Crash before
  call → re-spawn re-feeds the same row. No separate WAL table.
