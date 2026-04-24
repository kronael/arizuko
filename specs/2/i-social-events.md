---
status: shipped
---

> Renamed 2026-04-24: `react` → `like` for semantic alignment with platform
> UI (favourite/like/heart). Downvote counterpart (reddit, future) will be
> `dislike`, not `hate`.

# Social Events — Unified Inbound Model

Normalize inbound events into typed InboundEvent. Gateway filters
by impulse weights, routes by verb. Agents see a uniform stream.

## Verbs

`message, reply, post, like, repost, follow, join, edit, delete, close`

- `dislike` (future — for platforms with explicit downvote: reddit primarily)

## Platform mapping

### Chat channels (verb always `message`)

| Source            | content | thread   |
| ----------------- | ------- | -------- |
| Telegram chat msg | text    | -        |
| WhatsApp msg      | text    | -        |
| Discord msg       | text    | threadId |
| Web (slink)       | text    | -        |

### Reddit

| Source              | verb    | thread  | target     | mentions_me |
| ------------------- | ------- | ------- | ---------- | ----------- |
| DM received         | message | -       | -          | -           |
| Comment on our post | reply   | post_id | post_id    | -           |
| u/ mention          | message | post_id | comment_id | yes         |
| New post in r/sub   | post    | -       | -          | -           |
| Upvote on our post  | like    | -       | post_id    | -           |

### Mastodon / Bluesky

| Source            | verb    | thread    | target    | mentions_me |
| ----------------- | ------- | --------- | --------- | ----------- |
| DM (direct vis.)  | message | -         | -         | -           |
| @mention          | message | status_id | -         | yes         |
| Reply to our post | reply   | status_id | status_id | -           |
| Favourite/like    | like    | -         | status_id | -           |
| Boost/repost      | repost  | -         | status_id | -           |
| New follower      | follow  | -         | -         | -           |

### Email

| Source            | verb    | thread    | target |
| ----------------- | ------- | --------- | ------ |
| Direct email      | message | thread_id | -      |
| Reply in thread   | reply   | thread_id | msg_id |
| Mailing list post | post    | list_id   | -      |

## Impulse filter

Per-group weight-based batching between discovery and queue enqueue.
Each verb has an integer weight. Events accumulate impulse per group.
When sum >= threshold, flush to queue. Safety timeout flushes if
threshold never reached.

### Default weights

| Verb    | Default | Notes                        |
| ------- | ------- | ---------------------------- |
| message | 100     |                              |
| reply   | 100     |                              |
| post    | 100     | tune down if feed is noisy   |
| like    | 100     | tune to 5 for "20 = trigger" |
| repost  | 100     | tune to 10 if noisy          |
| follow  | 100     | tune to 10 if noisy          |
| close   | 100     | triggers thread lifecycle    |
| join    | 0       | ignored                      |
| edit    | 0       | ignored                      |
| delete  | 0       | ignored                      |

Weight 0 = drop. Operator sets `weights` and `threshold` per group.

### Flush delivery

- Immediate (weight >= threshold): individual message with full content.
- Batched (weight < threshold): plain text bracket summary,
  e.g. `[5 likes on post abc123, 3 reposts, 10 new followers]`.

## Agent XML format

```xml
<message sender="alice" time="..." platform="mastodon" verb="reply"
         thread="status_123" target="status_456">
  content
</message>
```

Attributes: `platform`, `verb` always. `mentions_me` when mentioned.
`thread`/`target` when set.

## JID format (shipped platforms)

| Platform | DM JID              | Feed JID             |
| -------- | ------------------- | -------------------- |
| Reddit   | `reddit:{username}` | `reddit:r_{sub}`     |
| Mastodon | `mastodon:{id}`     | `mastodon:{id}:feed` |
| Bluesky  | `bluesky:{did}`     | `bluesky:{did}:feed` |

## Decisions

- Batch summary is plain text in brackets, not XML.
- Like content is the platform-native string (emoji, "upvote", etc.).
- Auth failure: log error, mark channel disconnected, reconnect next tick.
