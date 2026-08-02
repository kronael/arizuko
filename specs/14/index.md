---
status: planned
---

# specs/14 — future features

Unscheduled feature work that still carries a design decision. Anything
here that is only a paragraph of intent lives as a line below, not as a
file.

| Spec                                           | Status | Hook                                                                                   |
| ---------------------------------------------- | ------ | -------------------------------------------------------------------------------------- |
| [1-pinned-messages.md](1-pinned-messages.md)   | draft  | Platform pins as durable agent context; re-injected across compaction, never stored.   |
| [4-route-drop.md](4-route-drop.md)             | draft  | `routes.effect='drop'` — firewall-style match-and-discard, stopping on first hit.      |
| [6-dynamic-channels.md](6-dynamic-channels.md) | draft  | Channels as DB rows with dashd-managed credentials; hybrid supervisor/per-row compose. |
| [7-auth-tunneling.md](7-auth-tunneling.md)     | draft  | Complete a platform challenge in a human browser, ship the credential to the daemon.   |

## Ideas without a spec

- **slink token → scoped auth session.** Route tokens work today
  (`store/migrations/0059-route-tokens.sql`). If a reason appears to unify
  them with `auth_sessions` — anonymous identity, group scope, revoke by
  row delete — it is a migration, not a design.

## Removed in the 2026-08-02 minimization

- `3-local-cli.md` — **deleted, shipped**. `arizuko send` is
  `cmd/arizuko/send.go` (`cmdSend`, registered in `cmd/arizuko/main.go:75`),
  with `--wait` / `--stream` / `--stdin` / `--topic` as specced. The
  implementation also moved past the spec: it posts to the `/chat/<token>`
  route-token endpoint or injects directly for the operator, rather than
  reading a `slink_token` out of the DB.
- `5-cli-chat.md` — **deleted, shipped**. `arizuko chat` is `cmdChat`
  (`cmd/arizuko/main.go:882`) and matches the spec exactly: validate the
  socket, require `claude` and `socat` on PATH, write a temp MCP config,
  exec `claude --mcp-config`.
- `2-slink.md` — **deleted**, reduced to the idea line above. Three
  sentences of deferral under a heading.
- `9-slink-typing.md` — **deleted**. A typing indicator on the web chat is
  the agent-working half of
  [16/3-shared-session-presence](../16/3-shared-session-presence.md), which
  specs it as a `working` frame on the existing hub. Two files, one concern.
- `8-cli-auth-helper.md` — **merged into**
  [7-auth-tunneling.md](7-auth-tunneling.md). It opened by saying the CLI
  and the dashd UI "are two faces of the same underlying auth-tunnel
  mechanism", then specified that mechanism a second time. One mechanism,
  one spec, with the CLI's genuine reasons (bootstrap before dashd is
  reachable, no browser over SSH, scriptable) kept.

`7-auth-tunneling.md` and the merged `8-cli-auth-helper.md` both cited
`8/32`, `8/37`, and `13/6-dynamic-channels.md` for the channel-row and
auth-tunnel work — a renumbering artifact. Those are `14/6` and `14/7`:
they were citing each other, and `14/7` was citing itself.
