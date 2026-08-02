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

`message, reply, post, like, repost, follow, join, edit, delete, close,
forward, quote, dislike`

- Inbound `forward` / `quote` / `dislike` are passthrough categories
  for platforms that emit them; routing has no special-case handling.

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

## Trigger gating (retired mechanism)

This spec originally paired the verb model with a per-group **impulse
filter**: integer weights per verb, accumulating until a threshold
flushed a batch. Two things about it were right and survive; the
mechanism itself does not.

Right: triggering is orthogonal to routing (a message can be stored in a
folder's scope without firing a turn), and the gate is universal —
`isSocialJid()` is gone, so no channel-type branch decides trigger
timing.

Retired: the weights themselves. `routes.impulse_config` was dropped in
migration `0054-route-target-fragment.sql`. Firing is now a `#observe`
fragment on `routes.target` plus explicit `verb=` match keys and `seq`
priority — [`../5/B-route-mode-ingestion.md`](../5/B-route-mode-ingestion.md).
A "quiet this verb" rule became a route row instead of a tuning knob,
which is legible in the routes table rather than buried in a JSON blob.

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

## Reaction events

When inbound `like` / `dislike` is triggered by a platform emoji
reaction (Discord MessageReactionAdd, Telegram message_reaction
update, WhatsApp messages.reaction), the adapter sets
`InboundMsg.Reaction` to the raw emoji and uses
`chanlib.ClassifyEmoji` to map sentiment:

- Negatives → `dislike`: 👎 💩 😡 🤬 💔 🤮 😢
- Everything else (including unknown emoji) → `like`

The adapter is signaling "someone reacted, here's what they used"; the
agent gets the actual emoji string for nuance. `Content` carries the
emoji as well so existing renderers without `Reaction` awareness still
display something meaningful.

## Decisions

- Batch summary is plain text in brackets, not XML.
- Like content is the platform-native string (emoji, "upvote", etc.).
- Auth failure: log error, mark channel disconnected, reconnect next tick.
