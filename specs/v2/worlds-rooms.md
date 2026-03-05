# Worlds, Rooms, and Threading — Research

Sources: brainpro (jgarzik), muaddib (pasky), ElizaOS, kanipi current model.

---

## 1. brainpro

**Repo**: https://github.com/jgarzik/brainpro
**Language**: Rust
**Shape**: coding assistant + WebSocket gateway for multi-channel delivery

brainpro is primarily a single-user agent with a multi-channel gateway bolted on.
It has no explicit Room or World concept. Rooms are implicit in the session key.

### Channel identity

```rust
// src/gateway/channels/plugin.rs
pub struct ChannelTarget {
    pub channel: String,   // "telegram" | "discord"
    pub chat_id: String,   // platform chat/channel ID
    pub user_id: Option<String>,
    pub username: Option<String>,
}

impl ChannelTarget {
    pub fn session_key(&self) -> String {
        format!("{}:{}", self.channel, self.chat_id)
    }
}
```

The session key `channel:chat_id` (e.g. `telegram:12345`) IS the room identifier.
It is never given a name or stored as a first-class entity — it exists only in the
`ChannelSessionMap` (in-memory HashMap keyed by `ChannelTarget`).

### Message shape

```rust
pub struct InboundMessage {
    pub target: ChannelTarget,
    pub content: String,
    pub message_id: String,
    // no threadId, no reply-to, no sender identity beyond target.user_id
}
```

Inbound messages carry no thread or reply metadata. Reply threading is not modelled.

### Session / history

```rust
// src/session.rs
pub struct SavedSession {
    pub session_id: String,
    pub messages: Vec<Value>,  // raw JSON message array
    pub turn_count: u32,
}
```

Sessions are persisted to `~/.brainpro/sessions/<id>.json`. History is a flat
message array — no per-room isolation beyond the session_id.

### Context / concurrency

Prioritized lane system for request scheduling:

```rust
pub enum LaneType { Cron, Main, Subagent, Batch }
```

Requests are queued into lanes with per-lane concurrency limits. The lane concept
lives entirely in the gateway; agents are stateless per-request.

### Summary

brainpro is a developer tool first. Its channel abstraction is minimal:
`channel:chat_id` → session. No Room entity, no World grouping, no thread
awareness, no multi-sender history. Good reference for the gateway/agent
protocol (NDJSON streaming, yield/resume, tool approval) but not for room modelling.

---

## 2. muaddib

**Repo**: https://github.com/pasky/muaddib
**Language**: TypeScript
**Shape**: IRC/Discord/Slack bot, single bot instance, multiple rooms

muaddib has the richest room model of the three. Its central concept is the **arc**
— a filesystem-safe composite identifier that encodes both server and channel.

### Arc — the room identity

```typescript
// src/rooms/message.ts
export interface RoomMessage {
  serverTag: string; // e.g. "irc.libera.chat" | "discord:MyServer" | "slack:MyWorkspace"
  channelName: string; // e.g. "#general" | "general" | "DM-nick"
  readonly arc: string; // computed: buildArc(serverTag, channelName)
  nick: string; // sender
  mynick: string; // bot's nick
  content: string;
  isDirect?: boolean; // mention/DM vs passive noise
  platformId?: string; // platform message ID
  threadId?: string; // platform thread ID
  responseThreadId?: string;
}

export function buildArc(serverTag: string, channelName: string): string {
  const raw = `${serverTag}#${channelName}`;
  return raw.replaceAll('%', '%25').replaceAll('/', '%2F');
  // e.g. "discord:MyServer#general"
}
```

The arc is both the session key and the filesystem path prefix for history:

```
~/.muaddib/arcs/
  discord:MyServer#general/
    chat_history/
      2025-01-01.jsonl
      2025-01-02.jsonl
    chronicle/
      cursor.json
  irc.libera.chat%23%23ai/
    chat_history/
      ...
```

### Session key — rooms + threading

Sessions (in-flight agent runs) are keyed by arc + sender OR arc + thread:

```typescript
// src/rooms/command/message-handler.ts
function sessionKey(message: RoomMessage): string {
  const arc = message.arc;
  if (message.threadId) {
    return `${arc}\0*\0${message.threadId}`; // thread-scoped
  }
  return `${arc}\0${message.nick.toLowerCase()}\0`; // sender-scoped
}
```

This means:

- In a non-threaded channel: each sender has their own session within the room
- In a threaded channel (Discord threads, Slack threads): the thread IS the session
- Sessions survive a few seconds; new messages steer the running agent rather
  than spawning a new one

### Message history scoping

`ChatHistoryStore` stores per-arc JSONL files (one file per date). Context
loading respects thread boundaries:

```typescript
async getContext(arc: string, limit?: number, threadId?: string): Promise<Message[]> {
  return threadId
    ? this.readThreadContext(arc, threadId, limit)  // thread replies + pre-thread context
    : this.readMainContext(arc, limit);              // main channel only, skips tid lines
}
```

Thread context: collect replies with `tid === threadId`, then walk backwards
collecting pre-thread main-channel lines up to `limit`. This gives the agent
both thread-local and broader channel context.

### Room configuration

Each room (by name, e.g. "irc", "discord") has a `RoomConfig`:

```typescript
// src/config/muaddib-config.ts
export interface RoomConfig {
  enabled?: boolean;
  command?: CommandConfig; // historySize, modes, rateLimit, ignoreUsers
  proactive?: ProactiveRoomConfig; // proactive interjection thresholds
  promptVars?: Record<string, string>;
  botName?: string;
  replyStartThread?: { channel?: boolean; dm?: boolean };
}
```

Rooms are named by transport ("irc", "discord", "slack"). There is no World
concept — a single muaddib instance typically serves one bot identity across
multiple rooms/servers.

### RoomGateway — transport abstraction

```typescript
// src/rooms/room-gateway.ts
export class RoomGateway {
  private readonly transports = new Map<string, TransportHandler>();

  register(transport: string, handler: TransportHandler): void { ... }

  async inject(arc: string, content: string): Promise<void> { ... }
  async send(arc: string, text: string): Promise<void> { ... }
}
```

`RoomGateway` decouples the command pipeline from specific transports. Any code
(scheduled events, heartbeats) can push synthetic commands or send messages to
any arc without knowing what transport backs it.

### Context reduction

When history exceeds the context window, muaddib uses an LLM-based reducer:

```typescript
// src/rooms/command/context-reducer.ts
export class ContextReducerTs {
  async reduce(
    context: Message[],
    agentSystemPrompt: string,
  ): Promise<Message[]>;
}
```

The reducer produces a condensed `[USER]/[ASSISTANT]` conversation that fits
the model's window. This is optional (disabled if `model` or `prompt` not set).

### Summary

muaddib's model: **arc = server#channel** as the primary room ID, sessions keyed
by arc+sender or arc+thread, history stored as per-arc JSONL files. No explicit
World concept — "server" is implicit in the `serverTag` prefix. Thread-first:
when `threadId` is present it completely replaces sender as the session scope.

---

## 3. ElizaOS

**Repo**: https://github.com/elizaOS/eliza
**Language**: TypeScript
**Shape**: framework with plugins per platform

ElizaOS has the most formal ontology: **World > Room > Entity > Memory**.

### Core hierarchy

```typescript
// packages/core/src/types.ts (reconstructed from docs/source)

interface World {
  id: UUID; // worldId
  name: string;
  agentId: UUID;
  serverId: string; // platform server ID (Discord guild, etc.)
  metadata?: Record<string, unknown>;
}

interface Room {
  id: UUID; // roomId
  name: string;
  worldId?: UUID; // which world this room belongs to
  source: string; // platform name: "discord", "telegram", ...
  type: RoomType; // GROUP | DM | VOICE | FEED | THREAD
  channelId?: string; // platform-native channel ID
  serverId?: string;
  metadata?: Record<string, unknown>;
}

interface Entity {
  id: UUID; // entityId
  agentId: UUID;
  names: string[];
  metadata?: Record<string, Record<string, unknown>>; // keyed by source
}

interface Memory {
  id: UUID;
  agentId: UUID;
  roomId: UUID;
  entityId: UUID;
  worldId?: UUID;
  content: Content;
  embedding?: number[];
  createdAt?: number;
}
```

### roomId construction per platform

Each platform client (Discord, Telegram, etc.) calls `ensureConnection()` which
upserts World, Room, and Entity records. roomId is a **deterministic UUID** derived
from the platform-native channel/chat ID:

```typescript
// pattern used in platform clients
const roomId = stringToUuid(`${channelId}-${agentId}`);
const worldId = stringToUuid(`${guildId ?? 'dm'}-${agentId}`);
```

- One World per Discord guild / Telegram group context
- One Room per Discord channel / Telegram chat
- DMs get their own World scoped to the user+agent pair

### Memory scoping

Memories are stored with `roomId`, `entityId`, and optionally `worldId`.
Queries can scope by any combination:

```typescript
await runtime.messageManager.getMemories({ roomId, count: 20 });
await runtime.messageManager.getMemories({ worldId, count: 50 });
```

The `messageManager` stores conversation history. A separate `knowledgeManager`
stores semantic memories (RAG). Both use roomId as the primary scope.

### Multi-channel

ElizaOS clients are adapters implementing a `Client` interface. Each platform
creates its own rooms and worlds. A single agent runtime can serve multiple
platforms simultaneously; rooms from different platforms never share IDs.

### Summary

ElizaOS World = platform server/guild/organization. Room = channel/chat/thread.
Entity = user or agent. Memory is scoped to (roomId, entityId). The hierarchy
supports multi-agent setups where multiple agents share a world but have separate
memory spaces.

---

## 4. kanipi current model

kanipi uses a flat JID-centric model:

```sql
-- chats table
jid TEXT PRIMARY KEY    -- e.g. "tg:-100123456" | "wa:+1234@g.us" | "dc:chan:guild"
channel TEXT            -- "telegram" | "whatsapp" | "discord" | "email"
is_group INTEGER

-- registered_groups table
jid TEXT PRIMARY KEY        -- prefix or exact JID
group_folder TEXT NOT NULL  -- maps to filesystem path (groups/<folder>/)
requires_trigger INTEGER

-- messages table
chat_jid TEXT               -- FK to chats
sender, content, timestamp, ...
```

The `GroupQueue` maintains in-memory state keyed by JID:

```typescript
// src/group-queue.ts
private groups = new Map<string, GroupState>();
// key is groupJid (the chat_jid value)
```

**What exists:**

- `chat_jid` — channel-native ID prefixed with platform (`tg:`, `wa:`, `dc:`)
- `group_folder` — agent group that handles this JID
- `registered_groups` — JID prefix → group_folder routing table

**What does not exist:**

- No Room entity with metadata
- No World grouping multiple chats
- No thread/reply-to tracking
- No session key that encodes sender (one session per JID, not per sender)
- No per-room history size config
- No context window management

---

## 5. Comparison

| Concept                | brainpro                          | muaddib                                  | ElizaOS                       | kanipi                         |
| ---------------------- | --------------------------------- | ---------------------------------------- | ----------------------------- | ------------------------------ |
| Room ID                | `channel:chat_id` (implicit)      | `arc` = `serverTag#channelName`          | UUID derived from platform ID | `chat_jid` (platform-prefixed) |
| World/Server           | none                              | implicit in `serverTag` prefix           | explicit `World` entity       | none                           |
| Thread support         | none                              | `threadId` → session scope               | `RoomType.THREAD`             | none                           |
| Reply-to               | none                              | `responseThreadId`                       | via content metadata          | none                           |
| History scope          | per session_id                    | per arc (JSONL files)                    | per roomId (DB)               | per chat_jid (DB)              |
| Context limit          | per turn (flat)                   | configurable `historySize` + LLM reducer | configurable count            | none                           |
| Multi-sender isolation | none                              | arc+nick session key                     | per entityId memory           | none (one session per JID)     |
| Room config            | none                              | `RoomConfig` per transport name          | plugin/client config          | none                           |
| Gateway abstraction    | `ChannelTarget` + `ChannelPlugin` | `RoomGateway` + `TransportHandler`       | `Client` interface            | channel modules (implicit)     |

---

## 6. What kanipi should adopt

### 6.1 Arc-style room ID (adopt from muaddib)

Replace bare `chat_jid` with a structured two-part identifier:

```
<platform>:<server_or_context>#<channel_or_chat>
```

Examples:

- `tg:@MyGroup#-100123456` (Telegram group)
- `wa:+1234567890@g.us#root` (WhatsApp group, no sub-channels)
- `dc:MyServer#general` (Discord server + channel)
- `email:inbox#thread-id` (email thread)

Benefits: the server/workspace is explicit, enabling future World-level queries.
kanipi's current `tg:-100123456` already embeds platform; this adds server context.

The `chat_jid` column can remain as the arc value — it just needs a richer format.

### 6.2 Thread / reply-to tracking (adopt from muaddib)

Add `thread_id` and `reply_to_id` columns to the messages table:

```sql
ALTER TABLE messages ADD COLUMN thread_id TEXT;      -- platform thread ID
ALTER TABLE messages ADD COLUMN reply_to_id TEXT;    -- message being replied to
```

Add `threadId` and `responseThreadId` to the IPC message format so the agent
can respond into the correct thread.

For channels that support threads (Discord, email), the `GroupQueue` session key
should become `jid + thread_id` rather than plain `jid`.

### 6.3 Per-room history size (adopt from muaddib)

The `registered_groups` table or a new `room_config` table should store:

```sql
CREATE TABLE room_config (
  jid TEXT PRIMARY KEY,
  history_size INTEGER NOT NULL DEFAULT 20,
  context_mode TEXT NOT NULL DEFAULT 'recent',  -- recent | summary | none
  requires_trigger INTEGER NOT NULL DEFAULT 0
);
```

This lets different rooms (a high-volume Discord channel vs a quiet Telegram group)
have different context window policies without code changes.

### 6.4 Sender-scoped sessions for group chats (adopt from muaddib)

In group chats where multiple users talk to the bot simultaneously, session
isolation should be per-sender, not per-room. muaddib's approach:

```
session key = arc + "\0" + nick.toLowerCase()
```

kanipi equivalent: the container name (or IPC session) should encode both `chat_jid`
and `sender` when `requires_trigger = 1`. When `requires_trigger = 0` (dedicated
room), the existing one-session-per-JID model is correct.

### 6.5 RoomGateway abstraction (partial adopt from brainpro/muaddib)

Currently each channel module (telegram.ts, whatsapp.ts, discord.ts) is called
directly from index.ts. A thin routing layer would let scheduled tasks and IPC
events send to any room without coupling to transport:

```typescript
// conceptual
interface RoomSender {
  send(jid: string, content: string, threadId?: string): Promise<void>;
}
```

This is a quality-of-life improvement, not a correctness fix. Low priority.

### 6.6 What ElizaOS adds over muaddib — and what to take

muaddib gives us: arc identity, thread-scoped sessions, per-arc JSONL history,
context reducer. That covers 80% of what kanipi needs.

ElizaOS adds three things on top:

**1. Entity tracking** — users are first-class records (`Entity`) with names,
platform IDs, and per-source metadata. muaddib tracks sender only as a string
in the session key. ElizaOS can answer "who is this user across platforms" and
merge cross-channel identity. kanipi currently stores `sender` as a string too.
**Take**: add a `senders` or `entities` table keyed by JID+platform when
cross-channel identity becomes a product need. Not now.

**2. World-scoped memory queries** — memories can be queried across all rooms
in a world (`worldId`), not just per-room. This enables "what happened in this
Discord server today" queries across all its channels. muaddib has no equivalent.
**Take**: the arc `serverTag` prefix already encodes the server — a world-scoped
query is just `WHERE jid LIKE 'dc:MyServer#%'`. No new abstraction needed;
the arc format (6.1) gives us this for free.

**3. Vector embeddings on every memory** — `Memory.embedding` enables semantic
similarity search across history. muaddib uses recency only (JSONL walk).
This is what powers eliza-atlas facts retrieval.
**Take**: this belongs in the v2 facts layer (`specs/v2/memory-facts.md`),
not in the room/session model. Do not add embeddings to the messages table.

---

## 7. Open questions

1. **Discord threads**: kanipi has Discord support. Should Discord thread IDs
   trigger sender-scoped sessions the same way muaddib does, or should each
   thread become its own registered JID? The latter fits kanipi's current model
   better.

2. **Email threading**: email already has natural thread IDs (In-Reply-To headers).
   The email channel likely needs `thread_id` tracking for correct reply routing
   before session-scoping matters.

3. **Multi-sender group chats**: the current model assumes one agent session per
   group. If two users address the bot simultaneously in the same group, they share
   a container. Is per-sender isolation needed, or is the group-level lock sufficient?
   This is a product question, not a technical one.

4. **Context window management**: kanipi currently passes all recent messages to
   the container agent. There is no cap. For high-volume rooms this will eventually
   hit token limits. muaddib's `historySize` config + LLM reducer is the right
   pattern. A simpler first step: add `history_size` to `registered_groups` and
   truncate message history in `container-runner.ts`.

5. **Arc format migration**: changing `chat_jid` format is a breaking change for
   existing instances. A migration that re-prefixes existing JIDs (e.g.
   `tg:-100123456` → `tg:direct#-100123456`) needs to handle `registered_groups`,
   `messages`, `sessions`, and `scheduled_tasks` tables atomically.

---

## References

- [brainpro](https://github.com/jgarzik/brainpro) — Rust agent gateway, lane system, channel plugin trait
- [muaddib](https://github.com/pasky/muaddib) — arc model, JSONL history, thread-scoped sessions
- [ElizaOS](https://github.com/elizaOS/eliza) — World/Room/Entity ontology, deterministic UUIDs
- [ElizaOS docs](https://docs.elizaos.ai) — framework overview and plugin patterns
