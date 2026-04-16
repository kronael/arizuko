---
status: draft
---

# Worlds, Rooms, and Threading — Research

Comparative analysis of room models in brainpro, muaddib, ElizaOS,
and arizuko's current design. Not started.

## Summary of prior art

- **brainpro** (Rust): no Room entity. `channel:chat_id` IS the
  session key. No threads, no world grouping. Multi-lane queue in
  gateway; agents stateless per-request.

- **muaddib** (TS): richest room model. `arc = serverTag#channelName`
  is the room ID and filesystem path prefix (`~/.muaddib/arcs/<arc>/`).
  Session key = `arc + nick` (non-threaded) or `arc + threadId`
  (threaded). Per-arc JSONL history. Context reducer collapses old
  history via LLM. `RoomConfig` per transport.

- **ElizaOS** (TS): formal ontology `World > Room > Entity > Memory`.
  Deterministic UUIDs from platform IDs. Memories scoped by
  (roomId, entityId, worldId). Supports embeddings per memory.

## arizuko current

Flat JID-centric model:

- `chats.jid` (e.g. `telegram:main/-100...`) — primary key
- `routes(seq, match, target, impulse_config)` — JID → folder mapping
- `messages.source` — adapter-of-record per inbound
- `GroupQueue` in-memory, keyed by JID

Missing: no Room entity, no World, no thread/reply-to, no sender-scoped
sessions, no per-room history limit.

## Comparison

| Concept                | brainpro       | muaddib               | ElizaOS               | arizuko           |
| ---------------------- | -------------- | --------------------- | --------------------- | ----------------- |
| Room ID                | `channel:id`   | `arc`                 | UUID from platform ID | `chat_jid`        |
| World/server           | none           | implicit in serverTag | explicit World entity | none              |
| Thread support         | none           | threadId → session    | RoomType.THREAD       | none              |
| History scope          | per session_id | per arc JSONL         | per roomId (DB)       | per chat_jid (DB) |
| Multi-sender isolation | none           | arc+nick              | per entityId          | none              |
| Context reducer        | no             | LLM reducer           | configurable count    | none              |

## What arizuko should adopt

**Arc-style ID** (from muaddib): replace bare `chat_jid` with
`<platform>:<server>#<channel>` — server-level queries fall out for
free via `WHERE jid LIKE 'dc:MyServer#%'`.

**Thread tracking** (from muaddib): add `thread_id` + `reply_to_id`
to messages. `GroupQueue` session key becomes `jid + thread_id` for
channels that support threads (Discord, email).

**Per-room history config**: `groups` table or new `room_config`
table with `history_size`, `context_mode`, `requires_trigger`.

**Sender-scoped sessions in group chats**: when `requires_trigger=1`,
session key includes sender. When `requires_trigger=0`, keep per-JID.

## Deferred (ElizaOS features arizuko does NOT need now)

- Entity tracking as first-class records (add only if cross-channel
  identity becomes a product need).
- Vector embeddings on messages (belongs in v2 facts layer, not
  the room model).
- World as explicit entity (arc format prefix already encodes it).

## Open questions

1. Discord threads: sender-scoped session or distinct JID?
2. Email thread_id tracking order vs session-scoping.
3. Context window management — add `history_size` + truncate in runner.
4. Arc format migration: breaks existing `chat_jid` values across
   `groups`, `routes`, `messages`, `sessions`, `scheduled_tasks`.

## References

- brainpro: https://github.com/jgarzik/brainpro (Rust, lane system)
- muaddib: https://github.com/pasky/muaddib (arc model, JSONL)
- ElizaOS: https://github.com/elizaOS/eliza (World/Room/Entity)
