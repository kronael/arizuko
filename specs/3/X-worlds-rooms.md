---
status: unshipped
---

# Worlds, Rooms, Threading — research

Comparative analysis of room models in brainpro, muaddib, ElizaOS,
arizuko.

## Prior art

- **brainpro** (Rust): no Room entity. `channel:chat_id` IS the session
  key. No threads, no world grouping. Stateless per-request.
- **muaddib** (TS): richest model. `arc = serverTag#channelName` is the
  room ID and filesystem prefix (`~/.muaddib/arcs/<arc>/`). Session key
  = `arc + nick` or `arc + threadId`. Per-arc JSONL history. LLM
  context reducer.
- **ElizaOS** (TS): formal `World > Room > Entity > Memory`. Deterministic
  UUIDs from platform IDs. Memories scoped by (roomId, entityId, worldId).

## Arizuko current

Flat JID-centric. `chats.jid` (e.g. `telegram:main/-100...`) is PK.
`routes(seq, match, target, impulse_config)` maps JID → folder.
`GroupQueue` in-memory keyed by JID.

Missing: Room entity, World, thread/reply-to, sender-scoped sessions,
per-room history limit.

## Comparison

| Concept                | brainpro       | muaddib               | ElizaOS               | arizuko           |
| ---------------------- | -------------- | --------------------- | --------------------- | ----------------- |
| Room ID                | `channel:id`   | `arc`                 | UUID from platform ID | `chat_jid`        |
| World/server           | none           | implicit in serverTag | explicit World entity | none              |
| Thread support         | none           | threadId → session    | RoomType.THREAD       | none              |
| History scope          | per session_id | per arc JSONL         | per roomId (DB)       | per chat_jid (DB) |
| Multi-sender isolation | none           | arc+nick              | per entityId          | none              |
| Context reducer        | no             | LLM reducer           | configurable count    | none              |

## Arizuko candidates to adopt

- **Arc-style ID**: replace bare `chat_jid` with
  `<platform>:<server>#<channel>`. Server-level queries fall out via
  `WHERE jid LIKE 'dc:MyServer#%'`.
- **Thread tracking**: add `thread_id` + `reply_to_id` to messages.
  `GroupQueue` key = `jid + thread_id` for threaded channels.
- **Per-room history config**: `room_config` table with `history_size`,
  `context_mode`, `requires_trigger`.
- **Sender-scoped sessions**: when `requires_trigger=1`, key includes
  sender; else per-JID.

## Deferred (ElizaOS features not needed now)

- Entity tracking as first-class records
- Vector embeddings on messages (v2 facts)
- Explicit World entity (arc prefix encodes it)

## Open

1. Discord threads: sender-scoped or distinct JID?
2. Email thread_id order vs session-scoping.
3. Context window management (`history_size` + runner truncation).
4. Arc format migration breaks existing `chat_jid` in `groups`,
   `routes`, `messages`, `sessions`, `scheduled_tasks`.

## References

- brainpro: https://github.com/jgarzik/brainpro
- muaddib: https://github.com/pasky/muaddib
- ElizaOS: https://github.com/elizaOS/eliza
