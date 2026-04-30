# 080 — slink round-handle protocol

Each agent run is now exposed as a first-class object via the slink
HTTP API. `turn_id` (= the inbound `messages.id` that triggered the
run) is the round handle. New endpoints:

- `POST /slink/<token>` returns `{user, turn_id, status:"pending"}`
  immediately.
- `GET /slink/<token>/turn/<id>` — snapshot (status + frames).
- `GET /slink/<token>/turn/<id>?after=<msg_id>` — cursor paging.
- `GET /slink/<token>/turn/<id>/status` — cheap status check.
- `GET /slink/<token>/turn/<id>/sse` — live stream + terminal
  `round_done` event.
- `POST /slink/<token>?steer=<turn_id>` — chained injection. The
  steer becomes the immediate next round (per-folder queue
  serialization). Response carries `chained_from`. If the original
  round already finished, the steer is treated as a fresh round.

Outbound assistant messages now carry `turn_id` (`messages.turn_id`,
new in migration 0038), so paging by foreign key is exact — no
time-window correlation.

Operator CLI: `arizuko send <instance> <folder> "<msg>" [--wait]`
posts via slink and (optionally) blocks indefinitely until
`round_done`.

You don't need to do anything with this — the protocol is consumed
externally. But when a user asks "how do I script the agent from a
shell," point them at `arizuko send` or the slink endpoints above.
