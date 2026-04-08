# Routing

How messages get from a chat to the right agent group, and how replies
get back.

## Route Table

The `routes` table maps chat JIDs to group folders. Each row is a rule.

| Column           | Type   | Purpose                                                  |
| ---------------- | ------ | -------------------------------------------------------- |
| `id`             | int PK | Auto-increment                                           |
| `jid`            | text   | Chat JID or platform prefix (e.g. `telegram:12345`)      |
| `seq`            | int    | Evaluation order (lower = earlier)                       |
| `type`           | text   | Rule type (see below)                                    |
| `match`          | text   | Pattern to match against — meaning depends on type       |
| `target`         | text   | Group folder to route to                                 |
| `impulse_config` | text   | Optional JSON — controls whether a match fires the agent |

A JID can have multiple routes. They are evaluated in `seq` order; first
match wins. Platform-prefix routes (e.g. `telegram:`) act as wildcards for
all JIDs on that platform. Per-JID routes are checked before prefix routes.

```sql
-- Example: telegram chat 12345 routes to atlas/content by default
INSERT INTO routes (jid, seq, type, match, target)
VALUES ('telegram:12345', 0, 'default', NULL, 'atlas/content');

-- Example: all Discord chats fall back to atlas/social
INSERT INTO routes (jid, seq, type, match, target)
VALUES ('discord:', 9999, 'default', NULL, 'atlas/social');
```

### Route lookup

`store.GetRoutes(jid)` returns all routes for a JID, ordered by
specificity (exact JID first, then platform prefix) and then `seq`:

```sql
SELECT * FROM routes WHERE jid = ? OR jid = ?  -- ? = jid, ? = platform prefix
ORDER BY CASE jid WHEN ? THEN 0 ELSE 1 END, seq ASC
```

## Route Types

`router.routeMatches(route, msg)` tests each type:

| Type      | Match field | Matches when                                          |
| --------- | ----------- | ----------------------------------------------------- |
| `default` | (empty)     | Always matches — catchall for a JID                   |
| `command` | `/code`     | Message starts with `/code` or equals `/code`         |
| `prefix`  | `@` or `#`  | Message contains `@name` or `#topic` (inline routing) |
| `verb`    | `join`      | Message verb equals the match (case-insensitive)      |
| `pattern` | regex       | Regex matches message content (max 200 chars)         |
| `keyword` | `help`      | Substring match on content (case-insensitive)         |
| `sender`  | regex       | Regex matches sender name                             |

### Target expansion

Targets can contain `{sender}`, which expands to a sanitized sender ID.
This creates per-user child groups:

```sql
-- Each Discord user gets their own group under atlas/
INSERT INTO routes (jid, seq, type, match, target)
VALUES ('discord:', 0, 'default', NULL, 'atlas/{sender}');
-- discord:alice → atlas/dc-alice, discord:bob → atlas/dc-bob
```

### Prefix routes (@ and #)

When a group is registered at tier <= 2, two prefix routes are
auto-inserted with negative `seq` values (so they evaluate before
user-defined routes):

```sql
INSERT INTO routes (jid, seq, type, match, target)
VALUES (?, -2, 'prefix', '@', ?),   -- @ prefix → parent folder
       (?, -1, 'prefix', '#', ?);   -- # prefix → parent folder
```

The prefix route itself does not route to the folder directly. Instead,
`findPrefixRoute` detects that the message contains `@name` or `#topic`,
and the gateway dispatches based on the parsed name:

- **`@name` in message content**: gateway looks up `<target>/<name>` as a
  child group. If found, delegates the message (with `@name` stripped) to
  that child group's agent.

  ```
  User sends: "@content write a blog post about cats"
  Prefix route match: @ → atlas
  Child lookup: atlas/content exists
  Delegation: "write a blog post about cats" → atlas/content agent
  ```

- **`#topic` in message content**: gateway runs the message (with `#topic`
  stripped) in a topic-scoped session within the parent group.

  ```
  User sends: "#support my account is locked"
  Prefix route match: # → atlas
  Agent runs: "my account is locked" in session (atlas, #support)
  ```

## Resolution Order

When a message arrives, the gateway resolves which group handles it
through several layers. The full resolution in `resolveTarget`:

```
1. Reply chain     — msg.ReplyToID set → look up routed_to on the
                     original message → route to that group
2. Sticky group    — chat has sticky_group set → route there
3. Route table     — router.ResolveRoute(msg, routes) → first match
4. Default group   — routes with type=default, no match field
```

If the resolved target differs from the current group AND is an
authorized child (same world, direct child), the message is delegated
to that group. Otherwise it stays in the current group.

### Authorization check

`router.IsAuthorizedRoutingTarget(source, target)` enforces:

- `root` group can route to anything
- Otherwise, target must share the same world (first path segment)
  and be a direct child of source (one level deeper, no deeper)

```
source="atlas"       target="atlas/content"    → allowed (direct child)
source="atlas"       target="atlas/content/x"  → denied (grandchild)
source="atlas"       target="other/content"    → denied (different world)
source="root"        target="atlas/content/x"  → allowed (root)
```

## Topic Routing

Topics provide thread isolation within a single group. Each topic gets
its own Claude session, reply chain, and message history.

### How topics are set

1. **Platform-native threads**: channel adapters map native thread IDs
   to `Message.Topic`. Telegram's `MessageThreadID`, Discord's thread
   channel ID, etc.

2. **Web channel**: web messages carry a topic slug directly. The JID
   format is `web:<folder>`, and topics arrive pre-set.

3. **`#topic` prefix routing**: when a message matches a `#` prefix
   route, the gateway runs the agent with topic `#name`.

4. **Sticky topic**: the `#topic` command sets a persistent topic
   for the chat (see Sticky Routing below).

### How topics affect processing

- **Session isolation**: `store.GetSession(folder, topic)` — each
  `(folder, topic)` pair has its own session ID. Resetting one topic's
  session leaves others untouched. `/new #support` resets only the
  `#support` session.

- **Reply chain isolation**: `store.GetLastReplyID(jid, topic)` — each
  topic tracks its own last-sent message ID for reply threading.

- **Web topic batching**: `processWebTopics` splits web messages by
  topic and runs one agent per topic, serially.

### Effective topic resolution

```go
func effectiveTopic(chatJid, msgTopic string) string {
    stickyTopic := store.GetStickyTopic(chatJid)
    if stickyTopic != "" {
        return stickyTopic
    }
    return msgTopic
}
```

Sticky topic overrides the message's native topic. Clear with `#`.

## Reply Routing

When a user replies to a bot message, the reply should go to the same
group that produced the original response — even if the default route
points elsewhere.

### How routed_to works

Every outbound bot message records which group produced it:

```go
// Every agent reply goes through the same PutMessage path:
store.PutMessage(core.Message{
    ChatJID:   chatJid,
    Sender:    groupFolder,
    RoutedTo:  groupFolder,
    Topic:     topic,
    FromMe:    true,
    BotMsg:    true,
    ...
})
```

The `messages` table has a `routed_to` column. When a user replies to a
message (platform provides `reply_to_id`), the gateway looks it up:

```go
func resolveTarget(msg, routes, selfFolder) string {
    // 1. Reply chain: follow the reply_to_id
    if msg.ReplyToID != "" {
        routedTo := store.RoutedToByMessageID(msg.ReplyToID)
        if routedTo != "" && routedTo != selfFolder {
            return routedTo
        }
    }
    // 2. Sticky group
    // 3. Route table
    // ...
}
```

### Example: reply routing in action

```
Routes: telegram:12345 → atlas (default)
Groups: atlas, atlas/content, atlas/social

1. User sends: "@content write about cats"
   → prefix route matches, delegates to atlas/content
   → atlas/content replies: "Here's a post about cats..." (routed_to=atlas/content)

2. User replies to that message: "make it shorter"
   → reply_to_id points to the bot message
   → store.RoutedToByMessageID → "atlas/content"
   → message routes to atlas/content again (not atlas)

3. User sends a new message (no reply): "hello"
   → no reply chain, no sticky
   → default route → atlas
```

### Reply chain for outbound threading

The gateway tracks the last-sent message ID per `(jid, topic)` to build
reply chains on the platform side:

```
User: "help"                   (msg_id=100)
Bot:  "Sure, what's up?"       (reply_to=100, sent_id=201)
Bot:  "I can help with..."     (reply_to=201, sent_id=202)
```

`store.GetLastReplyID(jid, topic)` provides the anchor for the next
reply. Updated in three cases:

1. **Bot sends a chunk** — `SetLastReplyID` to the bot's sent message ID
2. **Steering message** — when a follow-up is injected into a running
   container, the gateway sets `lastReplyID` to the steering message's ID
3. **New agent run** — starts from `firstMsgID` (triggering user message),
   then re-reads `GetLastReplyID` before each chunk to pick up mid-run
   steering updates

## Sticky Routing

Sticky commands let users lock a chat to a specific group or topic
without prefixing every message.

### Commands

A message is a sticky command when its **entire content** (trimmed) is
one of these — no additional text:

| Input            | Effect                                | Confirmation               |
| ---------------- | ------------------------------------- | -------------------------- |
| `@atlas/content` | Lock chat to `atlas/content` group    | `routing -> atlas/content` |
| `@`              | Clear sticky group, return to default | `routing reset to default` |
| `#support`       | Lock chat topic to `support`          | `topic -> support`         |
| `#`              | Clear sticky topic                    | `topic reset to default`   |

Sticky state is stored per-chat in the `chats` table (`sticky_group`,
`sticky_topic` columns). All users in the same chat share one sticky
state.

### Interaction with other routing

Sticky group is checked in `resolveTarget` after reply-chain routing
but before the route table. This means:

- Replying to a specific bot message still routes to the group that
  produced it (reply chain takes precedence)
- Sticky overrides the default route table
- Inline `@name` in a message with content (e.g. `@content do this`)
  still fires prefix routing for that one message — sticky persists

Bot messages and scheduler-injected messages ignore sticky state.

### State model

```sql
ALTER TABLE chats ADD COLUMN sticky_group TEXT;  -- group folder or NULL
ALTER TABLE chats ADD COLUMN sticky_topic TEXT;  -- topic string or NULL
```

## Full Message Flow Example

A concrete walkthrough from user message to agent reply.

### Setup

```
Instance: REDACTED
Groups: atlas (root), atlas/content (tier 2), atlas/social (tier 2)
Routes:
  telegram:12345  seq=-2  prefix  @  atlas
  telegram:12345  seq=-1  prefix  #  atlas
  telegram:12345  seq=0   default    atlas
```

### User sends "hello" to telegram:12345

```
1. teled receives Telegram update, POST /v1/messages to gated
2. store.PutMessage({id:"tg-789", chat_jid:"telegram:12345",
     sender:"telegram:67890", content:"hello", topic:""})
3. gateway.pollOnce():
   - store.NewMessages(["telegram:12345"], since) → [{id:"tg-789",...}]
   - groupForJid("telegram:12345") → atlas (via default route)
   - handleStickyCommand? No (not a sticky command)
   - handleCommand? No (not /new, /ping, etc.)
   - tryExternalRoute? No prefix match, resolveTarget returns ""
   - impulse gate? If enabled, checks weight threshold
   - queue.EnqueueMessageCheck("telegram:12345")
4. processGroupMessages("telegram:12345"):
   - store.MessagesSince("telegram:12345", agentCursor) → messages
   - groupBySender → one batch for telegram:67890
   - enrichAttachments (download any media)
   - router.FormatMessages → XML prompt
   - container.Run(atlas, prompt, ...) → docker run
   - Agent processes, produces output
   - makeOutputCallback sends reply via ch.Send("telegram:12345", text, replyTo, topic)
     and records it via store.PutMessage (is_bot_message=1, routed_to=atlas)
   - store.SetLastReplyID("telegram:12345", "", sentMsgID)
5. teled receives POST /send, delivers to Telegram API
```

### User sends "@content write about cats"

```
1-2. Same as above, message stored
3. gateway.pollOnce():
   - findPrefixRoute → matches @ prefix route
   - parsePrefix("@content write about cats") → name="content", rest="write about cats"
   - handlePrefixRoute: child = atlas/content, exists
   - delegateViaMessage("atlas/content", "write about cats", "telegram:12345")
4. delegateViaMessage:
   - store.PutMessage({chat_jid:"local:atlas/content",
       forwarded_from:"telegram:12345", content:"write about cats", ...})
   - queue.EnqueueMessageCheck("local:atlas/content")
   - Child agent runs, produces output
   - processSenderBatch sees forwarded_from → routes reply back to
     telegram:12345 with routed_to=atlas/content
```

### User replies to that bot message with "shorter please"

```
1-2. Message stored with reply_to_id pointing to bot's message
3. gateway.pollOnce():
   - tryExternalRoute:
     - findPrefixRoute? No @ or # in content
     - resolveTarget:
       - msg.ReplyToID set → store.RoutedToByMessageID → "atlas/content"
       - atlas/content != atlas (self) → return "atlas/content"
     - IsAuthorizedRoutingTarget("atlas", "atlas/content") → true
     - delegateViaMessage("atlas/content", "shorter please", "telegram:12345")
```

## Multi-Group Chat

A single chat JID can route messages to different groups based on
content, replies, and sticky state.

```
telegram:12345 routes:
  seq=-2  prefix  @  atlas           # @ inline routing
  seq=-1  prefix  #  atlas           # # topic routing
  seq=0   default    atlas           # catchall

Possible message flows in the same chat:
  "hello"                → atlas (default)
  "@content draft post"  → atlas/content (prefix @)
  [reply to content msg] → atlas/content (reply chain)
  "#support help me"     → atlas, session=#support (prefix #)
  "@atlas/social"        → sticky set to atlas/social
  "what's new"           → atlas/social (sticky override)
  "@"                    → sticky cleared, back to atlas
```

Each group runs its own agent with its own session. Reply chains and
topic sessions keep conversations isolated even within the same chat.
