---
status: shipped
---

# Group routing — the flat routes table

One flat `routes` table for the whole instance. No `jid` column, no
`type` column, no per-group table. Code: `router/router.go`,
`routd/`.

## Why flat

The predecessor keyed routes by JID and split them by kind
(prefix / command / keyword / pattern / sender). Every new addressing
idea needed a new column and a new branch. Collapsing to
`(seq, match, target)` made the match language the only extension
point — migration `store/migrations/0022-routes-match.sql` dropped the
kind columns and moved prefix/command handling into code, where it
already lived.

## Match language

`match` is a space-separated list of `key=glob` pairs; all pairs must
match for the row to fire, and an empty `match` is the wildcard.

| key        | resolves to                                       |
| ---------- | ------------------------------------------------- |
| `platform` | `core.JidPlatform(msg.ChatJID)` — e.g. `telegram` |
| `room`     | post-colon portion of `msg.ChatJID`               |
| `chat_jid` | full `msg.ChatJID`                                |
| `sender`   | `msg.Sender`                                      |
| `verb`     | `msg.Verb` (defaults to `"message"` at ingest)    |

Globs are Go `path.Match` (`*`, `?`, `[abc]`) — no regex, no substring,
case-sensitive. Evaluation is ascending `seq`, first match wins;
`seq=0` is the convention for a folder's primary inbound row, which
`delete_route` / `set_routes` refuse to remove
([`../4/10-ipc.md`](../4/10-ipc.md) §route mutation safety).

## Target grammar

`target` is a folder path unless it carries a known destination prefix
(`folder:` / `daemon:` / `builtin:`). `folder:` is optional so existing
rows never needed re-keying; only explicit daemon/builtin rows need a
prefix. RFC 6570 `{sender}` template expansion applies to folder
targets.

A `#mode` fragment on `target` controls whether the row _fires_ a turn
or only stores — [`../5/B-route-mode-ingestion.md`](../5/B-route-mode-ingestion.md).
That fragment replaced the `impulse_config` column, which migration
`0054-route-target-fragment.sql` dropped.

## Layers never share a namespace

Inbound flows through four ordered layers, and only the last one reads
the routes table:

1. **Sticky** (in-memory) — a bare `@name` / `#topic` message updates
   routing state ([`../3/a-sticky-routing.md`](../3/a-sticky-routing.md)).
2. **Command** (code table) — slash-prefixed first token.
3. **Prefix** (code) — inline `@name` / `#name` navigates relative to
   the message's owning folder ([`../4/23-topic-routing.md`](../4/23-topic-routing.md)).
4. **Routing** (data) — walks `routes` in `seq` order.

Keeping `@`/`#` out of the table is deliberate: they are relative to the
folder that owns the room, so a stored row would have to be duplicated
per group and re-written on every move.

## Registration

Group registration inserts exactly one row —
`(seq=0, match='room=<post-colon of jid>', target='<folder>')`. No
predefined prefix rows.

## Errors

No match: stored, not processed. Delegation failure: cursor advances,
error logged, no retry — the message stays in the DB for the parent to
read. Authorization is checked at resolution time by
`router.IsAuthorizedRoutingTarget`.
