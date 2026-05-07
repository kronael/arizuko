# Routing

How messages get from a chat to the right agent group, and how replies
get back.

## Platform JID prefixes

Each adapter declares the `platform:` prefixes it owns when it registers
with gated. The post-`:` portion is platform-private; routing predicates
treat it as an opaque string with `path.Match` glob semantics over `/`.

| Adapter | Prefix      | Example                                                             |
| ------- | ----------- | ------------------------------------------------------------------- |
| discd   | `discord:`  | `discord:<guild>/<channel>`, `discord:dm/<channel>`                 |
| teled   | `telegram:` | `telegram:user/<id>`, `telegram:group/<id>`                         |
| whapd   | `whatsapp:` | `whatsapp:1234567@s.whatsapp.net`                                   |
| mastd   | `mastodon:` | `mastodon:account/<id>`                                             |
| reditd  | `reddit:`   | `reddit:comment/<id>`, `reddit:user/<id>`, `reddit:submission/<id>` |
| bskyd   | `bluesky:`  | `bluesky:user/<did>`                                                |
| linkd   | `linkedin:` | `linkedin:user/<urn>`                                               |
| emaid   | `email:`    | `email:address/foo@bar.com`                                         |
| twitd   | `twitter:`  | `twitter:home`, `twitter:dm/<id>`                                   |
| webd    | `web:`      | `web:<folder>` (e.g. `web:atlas`)                                   |

`core.JidPlatform("twitter:dm/abc")` returns `twitter`; `core.JidRoom`
returns `dm/abc`. Use `platform=` predicates to match a whole platform
and `room=` globs to filter by kind/segment.

## Route Table

The `routes` table is a flat list of rules evaluated against every
inbound message. Schema (after migration 0022):

| Column           | Type   | Purpose                                                  |
| ---------------- | ------ | -------------------------------------------------------- |
| `id`             | int PK | Auto-increment                                           |
| `seq`            | int    | Evaluation order (lower = earlier)                       |
| `match`          | text   | Space-separated `key=glob` predicates (AND)              |
| `target`         | text   | Group folder to route to                                 |
| `impulse_config` | text   | Optional JSON — controls whether a match fires the agent |

There is no `jid` or `type` column. Rules are filtered entirely by the
`match` expression. Rules are evaluated in `seq` order; first rule whose
predicates all match wins.

```sql
-- All telegram chats default to atlas/content
INSERT INTO routes (seq, match, target)
VALUES (0, 'platform=telegram', 'atlas/content');

-- A specific telegram chat overrides
INSERT INTO routes (seq, match, target)
VALUES (-10, 'chat_jid=telegram:group/12345', 'atlas/legal');

-- All Discord chats fall back to atlas/social
INSERT INTO routes (seq, match, target)
VALUES (9999, 'platform=discord', 'atlas/social');
```

### Match expression

`router.RouteMatches` parses `match` as a whitespace-separated list of
`key=glob` predicates, AND'd together. An empty `match` matches every
message.

| Key        | Source                                 |
| ---------- | -------------------------------------- |
| `platform` | `core.JidPlatform(msg.ChatJID)`        |
| `room`     | `core.JidRoom(msg.ChatJID)` (post-`:`) |
| `chat_jid` | `msg.ChatJID` (full JID)               |
| `sender`   | `msg.Sender`                           |
| `verb`     | `msg.Verb` (e.g. `join`, `like`)       |

Glob semantics use `path.Match` over `/`-separated segments:

| Pattern      | Meaning                                       |
| ------------ | --------------------------------------------- |
| `key=exact`  | value equals `exact`                          |
| `key=<glob>` | value matches glob (`*` `?` `[…]`, `*` ≠ `/`) |
| `key=*`      | value is present (non-empty)                  |
| `key=`       | value is absent (empty)                       |
| omit key     | unconstrained — no filter on this field       |

```sql
-- Reddit submissions only (kind=submission), any sub
INSERT INTO routes (seq, match, target)
VALUES (0, 'platform=reddit verb=post', 'atlas/posts');

-- All twitter DMs (twitter:dm/<conv>) but not timeline/tweets
INSERT INTO routes (seq, match, target)
VALUES (0, 'platform=twitter room=dm/*', 'atlas/dm');
```

### Target expansion

Targets can contain `{sender}`, which expands to a sanitized sender ID.
This creates per-user child groups:

```sql
-- Each Discord user gets their own group under atlas/
INSERT INTO routes (seq, match, target)
VALUES (0, 'platform=discord', 'atlas/{sender}');
-- discord:user/alice → atlas/dc-alice, discord:user/bob → atlas/dc-bob
```

### Inline `@name` / `#topic` prefix layer

The `@` and `#` prefix layer is **not** in the routes table; it lives
in `gateway.handlePrefixLayer`. An anchored regex
(`^\s*@(\w[\w-]*)` / `^\s*#(\w[\w-]*)`) matches a sigil at the very
start of `msg.Content`; mid-content `@handle` or `#hashtag` (e.g.
forwarded tweets) do not trigger.

- **`@name`** at start of message: gateway looks up
  `<current-group>/<name>` as a child group. If found, delegates the
  message (with `@name` stripped) to that child group's agent.

  ```
  User in group atlas sends: "@content write a blog post about cats"
  parsePrefix → name="content", rest="write a blog post about cats"
  Child lookup: atlas/content exists
  Delegation: "write about cats" → atlas/content agent
  ```

- **`#topic`** at start of message: gateway runs the message (with
  `#topic` stripped) in a topic-scoped session within the current group.

  ```
  User in atlas sends: "#support my account is locked"
  Agent runs: "my account is locked" in session (atlas, #support)
  ```

If a referenced child group doesn't exist, the prefix is left in place
and the message falls through to the agent unchanged.

## Resolution Order

When a message arrives, the gateway resolves which group handles it
through several layers. The full resolution in `resolveTarget`:

```
1. Reply chain  — msg.ReplyToID set → look up routed_to on the
                  original message → route to that group
2. Sticky group — chat has sticky_group set → route there
3. Route table  — router.ResolveRoute(msg, routes) → first matching
                  rule wins (catchall = empty match expression)
```

The inline `@name` / `#topic` prefix layer is handled separately in
`gateway.handlePrefixLayer`, before route-table lookup.

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
Routes: match='platform=telegram' → atlas
Groups: atlas, atlas/content, atlas/social

1. User sends: "@content write about cats"
   → prefix layer matches, delegates to atlas/content
   → atlas/content replies: "Here's a post about cats..." (routed_to=atlas/content)

2. User replies to that message: "make it shorter"
   → reply_to_id points to the bot message
   → store.RoutedToByMessageID → "atlas/content"
   → message routes to atlas/content again (not atlas)

3. User sends a new message (no reply): "hello"
   → no reply chain, no sticky
   → route 'platform=telegram' → atlas
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

## HTTP Routing (proxyd)

proxyd evaluates each inbound HTTP request against a fixed list of path
prefixes, then falls through to a DB-backed `web_routes` table, then
applies a default auth-gate.

### Fixed prefixes (evaluated first, in order)

| Path prefix                                            | Behaviour                                 |
| ------------------------------------------------------ | ----------------------------------------- |
| `/slink/`                                              | public slink widget — no auth required    |
| `/invite/`                                             | onboarding invite flow — no auth required |
| `/p/`                                                  | persona pages — no auth required          |
| `/pub/`                                                | Vite public assets — no auth required     |
| `/chat/`, `/api/`, `/x/`, `/static/`, `/auth/`, `/mcp` | auth-gated, forwarded upstream            |

For `/api/` and `/x/` paths (and any request with `Accept: application/json`),
`requireAuth` returns `{"error":"unauthorized"}` with HTTP 401 instead of
redirecting to login.

### web_routes table (dynamic, DB-backed)

After fixed prefixes, proxyd looks up the longest matching prefix in the
`web_routes` table (migration `0045-web-routes.sql`):

| Column        | Type | Values                                  |
| ------------- | ---- | --------------------------------------- |
| `path_prefix` | TEXT | Primary key; longest-prefix wins        |
| `access`      | TEXT | `public` / `auth` / `deny` / `redirect` |
| `redirect_to` | TEXT | Used when `access = redirect`           |
| `folder`      | TEXT | Owning group folder                     |

Agents add/remove rows via the `set_web_route` / `del_web_route` /
`list_web_routes` MCP tools (registered in `ipc/ipc.go`). `MatchWebRoute`
in `store/web_routes.go` does the longest-prefix SQL query.

### Default (fallthrough)

Any path not matched by fixed prefixes or `web_routes` is auth-gated and
forwarded upstream (`requireAuth(servePub)`). Unknown paths redirect to
`/auth/login` for browsers; return 401 JSON for API/JSON clients.

## Full Message Flow Example

A concrete walkthrough from user message to agent reply.

### Setup

```
Instance: REDACTED
Groups: atlas (root), atlas/content (tier 2), atlas/social (tier 2)
Routes (in routes table):
  seq=0  match='platform=telegram'  target=atlas

Prefix layer (in gateway code, not table):
  @<name> at message start → delegate to atlas/<name>
  #<name> at message start → topic-scoped session in atlas
```

### User sends "hello" to telegram:12345

```
1. teled receives Telegram update, POST /v1/messages to gated
2. store.PutMessage({id:"tg-789", chat_jid:"telegram:12345",
     sender:"telegram:67890", content:"hello", topic:""})
3. gateway.pollOnce():
   - store.NewMessages(["telegram:12345"], since) → [{id:"tg-789",...}]
   - groupForJid("telegram:12345") → atlas (via 'platform=telegram' route)
   - handleStickyCommand? No (not a sticky command)
   - handleCommand? No (not /new, /ping, etc.)
   - handlePrefixLayer? No '@' or '#' at message start
   - tryExternalRoute? resolveTarget returns "" (already in target group)
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
   - handlePrefixLayer matches ^\s*@(\w[\w-]*)
   - parsePrefix("@content write about cats") → name="content", rest="write about cats"
   - child = atlas/content, exists
   - delegateViaMessage("atlas/content", "write about cats", "telegram:12345")
4. delegateViaMessage:
   - store.PutMessage({chat_jid:"atlas/content",
       forwarded_from:"telegram:12345", content:"write about cats", ...})
   - queue.EnqueueMessageCheck("atlas/content")
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
Routes table:
  seq=0  match='platform=telegram'  target=atlas    # catchall

Prefix layer is built-in; no table rows needed.

Possible message flows in the same chat:
  "hello"                → atlas (route)
  "@content draft post"  → atlas/content (prefix @)
  [reply to content msg] → atlas/content (reply chain)
  "#support help me"     → atlas, session=#support (prefix #)
  "@atlas/social"        → sticky set to atlas/social
  "what's new"           → atlas/social (sticky override)
  "@"                    → sticky cleared, back to atlas
```

Each group runs its own agent with its own session. Reply chains and
topic sessions keep conversations isolated even within the same chat.
