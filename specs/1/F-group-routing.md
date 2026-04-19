---
status: shipped
---

# Gateway Routing

Code: `router/`, `gateway/`. `daemon:` / `builtin:` target prefixes are
reserved but not yet implemented.

One flat routes table. Messages flow through three strictly ordered
layers in the gateway: commands (code), prefixes (code), routing (data).
Only the routing layer consults the routes table.

## Routing table

```sql
CREATE TABLE routes (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  seq            INTEGER NOT NULL DEFAULT 0,   -- evaluation order (lower first)
  match          TEXT    NOT NULL DEFAULT '',  -- space-separated key=glob pairs
  target         TEXT    NOT NULL,             -- folder path or typed target
  impulse_config TEXT                           -- optional per-row impulse JSON
);
CREATE INDEX idx_routes_seq ON routes(seq);
```

No `jid` column. No `type` column. One table for the whole instance.

## Match language

`match` is a space-separated list of `key=glob` pairs. All pairs must
match for the row to fire. An empty `match` matches every message
(wildcard).

Available keys resolve to fields on the incoming message:

| key        | resolves to                                       |
| ---------- | ------------------------------------------------- |
| `platform` | `core.JidPlatform(msg.ChatJID)` — e.g. `telegram` |
| `room`     | post-colon portion of `msg.ChatJID`               |
| `chat_jid` | full `msg.ChatJID`                                |
| `sender`   | `msg.Sender`                                      |
| `verb`     | `msg.Verb` (defaults to `"message"` at ingest)    |

Globs use Go `path.Match` semantics: `*`, `?`, `[abc]`. No regex. No
substring. No case-folding — match is case-sensitive.

### Examples

```
seq  match                                     target
0    room=REDACTED                          REDACTED/content
10   platform=telegram verb=mention            REDACTED/mentions
20   verb=follow                               REDACTED/notifs
30   platform=bluesky                          bsky/feed
99                                             default/firehose
```

## Target convention

`target` is a string. If it contains a `:` AND the prefix is a known
destination kind, the gateway dispatches accordingly. Otherwise it is
treated as a folder path.

| target                    | routed to                               |
| ------------------------- | --------------------------------------- |
| `REDACTED/content`        | folder (agent container)                |
| `folder:REDACTED/content` | same — `folder:` prefix is optional     |
| `daemon:onbod`            | HTTP POST to registered daemon (future) |
| `builtin:ping`            | in-gateway handler (future)             |

`folder:` is optional so existing rows don't need re-keying. Only new
explicit daemon/builtin rows need the prefix.

RFC 6570 `{sender}` template expansion still works for folder targets
(see `specs/4/9-gated.md` §template-routing).

## Pipeline

Every inbound message flows through four ordered layers in the
gateway. Each layer owns its namespace and does not consult the next.

1. **Sticky layer** (code, in-memory) — `handleStickyCommand` absorbs
   bare `@name` / `#topic` tokens as routing state updates.
2. **Command layer** (code, `gatewayCommands` table) — matches a
   slash-prefixed first token against the Go registration table in
   `gateway/commands.go`. One-line addition to add a new command.
3. **Prefix layer** (code) — inspects content for inline `@name` / `#name`
   tokens against the message's owning group folder; navigates to a
   child folder or synthesizes a topic session. No DB lookup.
4. **Routing layer** (data, `routes` table) — walks all rows in `seq`
   order and returns the first target whose `match` evaluates true for
   the current message. Used by `groupForJid` (which folder owns this
   room) and `tryExternalRoute` (delegate to a sibling).

Commands never touch routes. Prefixes never touch routes. Only the
routing layer reads or writes the routes table.

## Authorization boundaries

Root world groups (`root`, `root/*`) can delegate anywhere. Others can
only delegate to descendants in the same world. Enforced by
`IsAuthorizedRoutingTarget`.

IPC routing actions: tier 0 modifies any routes, tier 1 its own
subtree only, tier 2+ cannot modify routes.

### Self-harm guard

The IPC `delete_route` / `set_routes` handlers refuse to remove the
last route whose target equals the caller's own folder. Prevents an
agent from disconnecting itself from every inbound source.

## Error handling

- **No match**: message stored, not processed; debug log.
- **Delegation failure**: cursor advances (marked processed), error
  logged, no retry; message still in DB for parent access.
- **Authorization**: checked at runtime during route resolution.

## Registration

On group registration the gateway inserts one row:

```sql
INSERT OR IGNORE INTO routes (seq, match, target)
VALUES (0, 'room=<post-colon of jid>', '<folder>');
```

No predefined `@`/`#` prefix rows — the prefix layer handles those
entirely in code against the message's owning group folder.
