---
status: draft
---

## <!-- trimmed 2026-03-15: TS removed, rich facts only -->

## status: shipped

# Social Events — Unified Inbound Model

Normalize inbound events into typed InboundEvent. Gateway filters
by impulse weights, routes by verb. Agents see uniform stream.

## Verbs

message, reply, post, react, repost, follow, join, edit, delete, close

## Platforms

telegram, whatsapp, discord, email, web, reddit, twitter, mastodon,
bluesky, twitch, youtube, facebook, instagram, threads, linkedin

## Platform Mapping

### Chat channels (verb always Message)

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

### Close events

| Platform | Signal                   |
| -------- | ------------------------ |
| Discord  | thread archived/locked   |
| Reddit   | post locked by moderator |
| YouTube  | live stream ends         |
| Twitch   | stream goes offline      |
| Twitter  | reply restrictions set   |
| Facebook | comments disabled        |

Mastodon and Bluesky have no close concept.

## Impulse Filter

Per-group weight-based batching between message discovery and queue
enqueue. Each verb has integer weight. Events accumulate impulse per
group. When sum >= threshold, flush to queue. Safety timeout flushes
if threshold never reached.

### Default weights

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

Weight 0 = drop. Operator configures `weights` and `threshold` per group.

### Flush delivery

Immediate events (weight >= threshold): individual messages with full
content. Batched events (weight < threshold): plain text summary in
brackets: `[5 reactions on post abc123, 3 reposts, 10 new followers]`

## Verb Routing

Rule evaluation: command > verb > pattern > keyword > sender > default.

## Agent XML Format

```xml
<message sender="alice" time="..." platform="mastodon" verb="reply"
         thread="status_123" target="status_456">
  content here
</message>
```

Attributes: `platform` (always), `verb` (always), `mentions_me` (when
mentioned), `thread`/`target` (when set). Additive to existing format.

## JID Format

| Platform  | DM JID                     | Feed JID                       |
| --------- | -------------------------- | ------------------------------ |
| Reddit    | `reddit:{username}`        | `reddit:r_{sub}`               |
| Twitter   | `twitter:{userId}`         | `twitter:{userId}:feed`        |
| Mastodon  | `mastodon:{id}`            | `mastodon:{id}:feed`           |
| Bluesky   | `bluesky:{did}`            | `bluesky:{did}:feed`           |
| Twitch    | `twitch:{channel}`         | -                              |
| YouTube   | `youtube:{channelId}:live` | `youtube:{channelId}:comments` |
| Facebook  | `facebook:{pageId}`        | `facebook:{pageId}:feed`       |
| Instagram | `instagram:{threadId}`     | `instagram:biz:{accountId}`    |
| Threads   | `threads:{userId}`         | -                              |
| LinkedIn  | `linkedin:page:{pageId}`   | -                              |

## Decisions

- Batch summary: plain text brackets (not XML)
- React content: string — platform-native value (emoji, "upvote", etc.)
- Auth failure: log error, mark channel disconnected, reconnect next tick
