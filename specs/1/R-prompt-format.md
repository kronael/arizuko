---
status: shipped
---

# Prompt format

Stdin/stdout contract between the host and the in-container agent.
Assembled by `router/` + `container/runner.go`, consumed by `ant/`.

## ContainerInput (stdin JSON)

Canonical shape is the `Input` struct in `container/runner.go` — read it
there rather than duplicating the field list. Two fields carry a
decision worth recording:

- `persona` — `PERSONA.md` content for the group, when present. (It was
  called `soul` on the wire until the persona model settled; see
  [`../4/P-personas.md`](../4/P-personas.md).)
- `systemMd` — `SYSTEM.md` replaces the default system prompt outright
  rather than appending to it. A group that ships one owns its whole
  system prompt; there is no merge, because a half-overridden prompt is
  the shape nobody can debug.

`secrets` is stripped from the struct before any logging or persistence
(`container/runner.go` nils it after the env build).

Grants are **not** in the container input. Capability is checked at the
MCP socket per call, not handed to the agent as data it could read or
replay — [`../4/10-ipc.md`](../4/10-ipc.md).

## Assembly order

```
clock → system messages → pendingArgs → message history
```

`pendingArgs` is the raw text following a command trigger (`/ask what is
X` → `"what is X"`). Consumed once, deleted after read.

## Per-turn output — `submit_turn`

Results return over the same MCP unix socket via the `submit_turn`
JSON-RPC method, hidden from `tools/list`:

```jsonc
{
  "method": "submit_turn",
  "params": {
    "turn_id": "<originating message id>",
    "session_id": "abc123",
    "status": "success",
    "result": "...",
  },
}
```

One `submit_turn` per turn. `turn_id` + folder is the idempotency key.
An empty or missing `result` means the turn was deliberately silent;
`<internal>` tags are stripped before delivery.

Making the result a method call rather than parsed stdout is what makes
silence expressible: a stdout-marker protocol cannot distinguish "no
output" from "crashed before printing".

The delivery side of this contract has broken silently twice — once when
`recordTurnResult` dropped the result outright, once when interim `⏳`
status rows (also `is_bot_message=1`) made `TurnHasBotReply` think a
reply had already landed, so the real answer was skipped on every
multi-step turn. Both times health checks, dispatch, and `round_done`
stayed green while users got nothing. Verify this path by asserting a
bot row lands, never by asserting the turn completed.

## IPC close sentinel

An empty `_close` file (no extension) in `/run/ipc/input/` ends the
agent loop.
