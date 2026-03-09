# Social Events — Unified Inbound Model

**Status**: open

Normalize inbound events from all platforms into a typed
event struct. Gateway filters by impulse weights and routes
by verb. Agents see a uniform stream.

## InboundEvent

Replaces `NewMessage` as the gateway's internal event type.
Existing `NewMessage` fields map 1:1 (additive change).

```typescript
const Verb = {
  Message: 'message',
  Reply: 'reply',
  Post: 'post',
  React: 'react',
  Repost: 'repost',
  Follow: 'follow',
  Join: 'join',
  Edit: 'edit',
  Delete: 'delete',
  Close: 'close',
} as const;

type Verb = (typeof Verb)[keyof typeof Verb];

const Platform = {
  Telegram: 'telegram',
  WhatsApp: 'whatsapp',
  Discord: 'discord',
  Email: 'email',
  Web: 'web',
  Reddit: 'reddit',
  Twitter: 'twitter',
  Mastodon: 'mastodon',
  Bluesky: 'bluesky',
  Twitch: 'twitch',
  YouTube: 'youtube',
  Facebook: 'facebook',
  Instagram: 'instagram',
  Threads: 'threads',
  LinkedIn: 'linkedin',
} as const;

type Platform = (typeof Platform)[keyof typeof Platform];

interface InboundEvent {
  id: string;
  platform: Platform;
  jid: string;
  sender: string;
  sender_name?: string;
  timestamp: string;

  verb: Verb;
  content?: string;

  // thread position
  thread?: string;
  parent?: string;
  root?: string;

  // object acted upon
  target?: string;
  target_author?: string;

  // mentions
  mentions?: string[]; // user IDs/names mentioned in content
  mentions_me?: boolean; // true if agent is mentioned

  // compat
  is_from_me?: boolean;
  is_bot_message?: boolean;
  forwarded_from?: string;
}
```

Verb and Platform are const objects — typed at compile time,
serialized as strings in JSON/IPC/DB.

## Platform mapping

### Chat channels (existing — verb is always Message)

| Source            | verb    | content | thread   |
| ----------------- | ------- | ------- | -------- |
| Telegram chat msg | Message | text    | -        |
| WhatsApp msg      | Message | text    | -        |
| Discord msg       | Message | text    | threadId |
| Web (slink)       | Message | text    | -        |

### Reddit

| Source              | verb    | thread  | target     | mentions_me |
| ------------------- | ------- | ------- | ---------- | ----------- |
| DM received         | Message | -       | -          | -           |
| Comment on our post | Reply   | post_id | post_id    | -           |
| u/ mention          | Message | post_id | comment_id | yes         |
| New post in r/sub   | Post    | -       | -          | -           |
| Upvote on our post  | React   | -       | post_id    | -           |

### Twitter/X

| Source             | verb    | thread   | target   | mentions_me |
| ------------------ | ------- | -------- | -------- | ----------- |
| DM received        | Message | -        | -        | -           |
| @mention tweet     | Message | tweet_id | -        | yes         |
| Reply to our tweet | Reply   | tweet_id | tweet_id | -           |
| Like on our tweet  | React   | -        | tweet_id | -           |
| Retweet            | Repost  | -        | tweet_id | -           |
| New follower       | Follow  | -        | -        | -           |

### Mastodon / Bluesky

| Source            | verb    | thread    | target    | mentions_me |
| ----------------- | ------- | --------- | --------- | ----------- |
| DM (direct vis.)  | Message | -         | -         | -           |
| @mention          | Message | status_id | -         | yes         |
| Reply to our post | Reply   | status_id | status_id | -           |
| Favourite/like    | React   | -         | status_id | -           |
| Boost/repost      | Repost  | -         | status_id | -           |
| New follower      | Follow  | -         | -         | -           |

### Email

| Source            | verb    | thread    | target |
| ----------------- | ------- | --------- | ------ |
| Direct email      | Message | thread_id | -      |
| Reply in thread   | Reply   | thread_id | msg_id |
| Mailing list post | Post    | list_id   | -      |

### Twitch / YouTube

| Source           | verb    | thread    | target   |
| ---------------- | ------- | --------- | -------- |
| Chat message     | Message | stream_id | -        |
| Comment on video | Reply   | video_id  | video_id |
| New follower/sub | Follow  | -         | -        |

### Facebook / Instagram / Threads

| Source          | verb    | thread  | target  | mentions_me |
| --------------- | ------- | ------- | ------- | ----------- |
| Messenger DM    | Message | -       | -       | -           |
| Comment on post | Reply   | post_id | post_id | -           |
| @mention        | Message | -       | post_id | yes         |
| Page reaction   | React   | -       | post_id | -           |

### Close events (thread lifecycle)

| Platform | Signal                   | How                    |
| -------- | ------------------------ | ---------------------- |
| Discord  | thread archived/locked   | `THREAD_UPDATE` event  |
| Reddit   | post locked by moderator | `locked: true` on post |
| YouTube  | live stream ends         | stream status event    |
| Twitch   | stream goes offline      | offline event          |
| Twitter  | reply restrictions set   | reply settings change  |
| Facebook | comments disabled        | comment setting change |

Mastodon and Bluesky have no close concept.

## Impulse filter

Separate module: `src/impulse.ts`. Pure function, no side
effects, no DB, no platform knowledge. Gateway calls it
between message discovery and queue enqueue.

Each verb has an integer weight. Events accumulate impulse
per group. When sum ≥ threshold, flush pending events to
the group queue. Safety timeout flushes even if threshold
never reached.

```typescript
interface ImpulseConfig {
  threshold: number; // default: 100
  weights: Partial<Record<Verb, number>>; // per-verb weight
  max_hold_ms: number; // safety flush (default: 300000)
}

interface ImpulseState {
  pending: InboundEvent[];
  impulse: number;
  last_flush: number; // timestamp ms
}

interface FlushResult {
  events: InboundEvent[]; // all pending events
  immediate: InboundEvent[]; // weight ≥ threshold (full delivery)
  batched: InboundEvent[]; // weight < threshold (summary)
}

function accumulate(
  state: ImpulseState,
  event: InboundEvent,
  config: ImpulseConfig,
): { state: ImpulseState; flush: FlushResult | null };

function checkTimeout(
  state: ImpulseState,
  config: ImpulseConfig,
): FlushResult | null;
```

### Default weights

All verbs default to 100 (immediate). Operator tunes down
what's noisy for their use case. The gateway doesn't know
which events matter — that depends on what sources were
configured and why.

| Verb    | Default | Notes                        |
| ------- | ------- | ---------------------------- |
| Message | 100     |                              |
| Reply   | 100     |                              |
| Post    | 100     | tune down if feed is noisy   |
| React   | 100     | tune to 5 for "20 = trigger" |
| Repost  | 100     | tune to 10 if noisy          |
| Follow  | 100     | tune to 10 if noisy          |
| Close   | 100     | triggers thread lifecycle    |
| Join    | 0       | ignored                      |
| Edit    | 0       | ignored                      |
| Delete  | 0       | ignored                      |

Weight 0 = drop. Operator configures `weights` and
`threshold` per group to taste.

### Where it sits in code

```
index.ts startMessageLoop()
  getNewMessages()           // poll DB for new messages
  → for each group:
    → impulse.accumulate()   // <-- new, per-group state
    → if flush:
        resolveRoutingTarget()
        queue.enqueueMessageCheck()
  → impulse.checkTimeout()   // <-- new, end of loop tick
```

The impulse state lives in a `Map<string, ImpulseState>`
keyed by group JID, alongside the existing message loop
state. Existing chat channels have weight 100 for Message,
so every message flushes immediately — zero behavior change.

### Flush delivery

Immediate events (weight ≥ threshold) are delivered as
individual messages with full content. Batched events
(weight < threshold) are formatted as a summary line
appended to the prompt:

```
[5 reactions on post abc123, 3 reposts, 10 new followers]
```

## Routing by verb

Existing routing rules gain a `verb` type:

```typescript
| { type: 'verb'; verb: Verb; target: string }
```

Evaluation order: command → verb → pattern → keyword →
sender → default.

```json
[
  { "type": "verb", "verb": "post", "target": "main/feed" },
  { "type": "verb", "verb": "reply", "target": "main/support" },
  { "type": "verb", "verb": "message", "target": "main/dm" }
]
```

## Agent-facing XML format

`formatMessages()` in `router.ts` adds new attributes to
the XML header so the agent knows context:

```xml
<message sender="alice" time="..." platform="mastodon" verb="reply"
         thread="status_123" target="status_456">
  content here
</message>

<message sender="bob" time="..." platform="telegram" verb="message"
         mentions_me="true">
  @agent what do you think?
</message>
```

- `platform` — always present (telegram, discord, mastodon...)
- `verb` — always present (message, reply, post, react...)
- `mentions_me` — present when agent is mentioned
- `thread`, `target` — present when set

Existing chat channels emit `verb="message"` with the
platform name. No behavior change for current agents —
new attributes are additive.

## Thread context

Thread fields (`thread`, `parent`, `root`) are data on the
event. The agent sees them. The router can match on them
via pattern rules. If routing resolves to a non-existent
group, prototypes spawn it (`F-prototypes.md`).

## Migration from NewMessage

Additive. No breaking change.

1. Add `verb: Verb` and `platform: Platform` to `NewMessage`
2. Add optional `thread`, `parent`, `root`, `target`,
   `target_author`
3. Existing channels set `verb: Verb.Message`
4. Rename `NewMessage` → `InboundEvent` when stable

Field mapping:

- `chat_jid` → `jid`
- `replyTo` → `parent`
- `reply_to_text`, `reply_to_sender` → dropped (DB lookup)

## Outbound actions

See `T-social-actions.md`.

## JID format

| Platform  | DM JID                     | Feed JID                        |
| --------- | -------------------------- | ------------------------------- |
| Reddit    | `reddit:u_{user}`          | `reddit:r_{sub}`                |
| Twitter   | `x:{userId}`               | `x:{userId}:feed`               |
| Mastodon  | `mastodon:{instance}:{id}` | `mastodon:{instance}:{id}:feed` |
| Bluesky   | `bsky:{did}`               | `bsky:{did}:feed`               |
| Twitch    | `twitch:{channel}`         | -                               |
| YouTube   | `yt:{channelId}:live`      | `yt:{channelId}:comments`       |
| Facebook  | `fb:{threadId}`            | `fb:page:{pageId}`              |
| Instagram | `ig:{threadId}`            | `ig:biz:{accountId}`            |
| Threads   | `threads:{userId}`         | -                               |
| LinkedIn  | `li:page:{pageId}`         | -                               |

## Open

- Batch summary format — XML tags or plain text brackets?
- React `content` field carries reaction value (upvote, emoji)
- Platform auth failure handling (disable, retry, alert)
