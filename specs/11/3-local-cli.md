---
status: planned
---

# Local CLI — `arizuko send`

A CLI tool for local programs/scripts to send messages to groups.
Trivial wrapper over the slink round-handle protocol
([../1/W-slink.md](../1/W-slink.md)) — no separate transport, no new
auth, no new schema.

## Use cases

- Cron jobs reporting results to a group
- CI/CD pipelines notifying a group
- Scripts piping output to the agent
- Local dev tools interacting with groups
- Monitoring alerts routed to operator groups

## Shape

```bash
# fire and forget — print the turn_id, exit 0
arizuko send <instance> <folder> "deploy completed"

# wait for the round to finish, stream assistant frames to stdout
arizuko send <instance> <folder> "what's the status?" --wait

# stream as SSE (lower latency, same content)
arizuko send <instance> <folder> "..." --stream

# pipe stdin as message body
tail -n 20 /var/log/app.log | arizuko send <instance> <folder> --stdin

# continue a conversation thread (reuse the same topic across calls)
arizuko send <instance> <folder> "what about errors?" --topic debug
```

Exit codes: `0` on `status=done`, `1` on `failed`, `124` on `--wait`
timeout, `2` on usage / network errors.

## Resolved design

Earlier drafts of this spec listed open questions about transport,
auth, and JID format. The slink round-handle protocol settles them:

- **Transport**: HTTP to local webd over loopback. No new daemon, no
  separate socket, no direct sqlite writes from the CLI.
- **Auth**: server-side CLI reads the instance's `groups.slink_token`
  for the target folder directly out of `messages.db`. The token is
  what slink already uses for HTTP auth, so once the CLI has it, it
  hits `/slink/<token>` like any other client.
- **JID format**: messages arrive on `web:<folder>` (the same chat
  slink uses). Sender is `cli:<hostname>` (set on the CLI side via
  the form `sender_name`).
- **Streaming**: SSE on `/slink/<token>/turn/<id>/sse` — round-scoped,
  closes cleanly on `round_done`. Polling via `?after=<msg_id>` for
  cron-style callers that don't want a long-lived connection.
- **Multi-group / read / chat**: out of scope for the first ship.
  `arizuko chat` already exists as a separate command (root MCP
  socket); `read` is doable later via `inspect_messages`.

## Implementation

~80 LOC: open `messages.db`, look up `slink_token` for the folder,
HTTP POST to `127.0.0.1:<webd-port>/slink/<token>` with the message,
then either return immediately (default) or poll/stream the round.
No new server-side code.
