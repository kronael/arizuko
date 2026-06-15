---
status: draft
---

# Social Adapter Model

Social adapters (bskyd, mastd, reditd, twitd, linkd) operate differently from
chat adapters (teled, discd, slakd, whapd, emaid). This spec defines the
boundary; the observer system that handles broad monitoring is spec'd
separately in `6/`.

## Problem

Chat adapters handle bidirectional conversation: a user messages, the bot
replies, the thread continues. The adapter sees the full interaction landscape
because platforms push events to the bot.

Social platforms are different:

1. **Asymmetric visibility.** The bot sees only its own notifications (mentions,
   replies, DMs). It doesn't see the broader conversation unless it actively
   polls — which is rate-limited, expensive, and often against ToS.

2. **Selective engagement.** Not every mention deserves a reply. A chat message
   in a private channel is always relevant; a public @mention from a stranger
   may be spam, bait, or irrelevant. The agent needs discretion.

3. **Context lives outside the thread.** A reply to a viral tweet needs context
   from the thread, the quoted tweet, the author's history — none of which
   arrives in the notification payload.

4. **Observation is unbounded.** Monitoring "what's happening" on a social
   platform (trending topics, competitor mentions, sentiment) is a different
   problem from "respond to this notification." Bundling both into one adapter
   conflates two concerns with different scaling, auth, and trust profiles.

## Design: Two-System Split

### Adapter (this spec, 5/)

The social adapter covers the **immediate interaction landscape** only:

- Direct mentions of the bot account
- Replies to the bot's own posts
- DMs (where the platform supports them)
- Reactions/likes on the bot's posts (as `verb=like`)

This is the same event-driven model as chat adapters: platform pushes (or
adapter polls) notifications, adapter forwards to routd, agent replies via
`send`/`reply`. The adapter is thin; it doesn't crawl, doesn't observe, doesn't
index.

**Key difference from chat adapters:**

| Aspect             | Chat adapter           | Social adapter        |
| ------------------ | ---------------------- | --------------------- |
| Engagement default | Always reply           | Agent decides         |
| Context available  | Full thread in payload | Notification only     |
| Polling            | Rare (webhooks)        | Common (rate-limited) |
| Trust model        | Authenticated users    | Public strangers      |

### Observer (separate spec, 6/)

The observer handles **broad monitoring** — everything beyond the notification
stream:

- Keyword/hashtag monitoring
- Competitor/industry mentions
- Sentiment tracking
- Trend detection
- Thread context enrichment

The observer is a **different system entirely**:

- Runs outside the adapter, possibly outside arizuko
- May use different auth (read-only API keys, scraping, third-party services)
- Writes to a facts/knowledge store the agent reads, not to the message stream
- Has its own rate-limit budget, retry logic, and failure modes

The observer delivers context to the agent via files (`facts/`, `workspace/`)
or via a dedicated MCP tool (`get_social_context`), NOT by injecting messages
into the channel adapter's inbound stream.

## Adapter Behavior Changes

### 1. Selective reply (agent-driven)

Social adapters do NOT auto-route every notification to the agent. Instead:

```
notification → adapter → routd (stored, verb=mention) → routing rules
```

The routing rule for social channels should default to a **triage skill** that
decides whether to engage:

```
# routes table
chat_jid LIKE 'bsky:%'  →  folder=social/bsky  skill=triage
```

The triage skill sees the notification, checks relevance (is this spam? is
this a known account? is this on-topic?), and either:

- Replies directly
- Escalates to a human
- Ignores (no response)

This is different from chat adapters where the default is "always engage."

### 2. Minimal context in payload

The adapter sends only what the platform's notification API provides:

```go
InboundMsg{
    ID:          notificationID,
    ChatJID:     "bsky:user/did:plc:abc123",
    Sender:      "bsky:user/did:plc:xyz789",
    SenderName:  "@alice.bsky.social",
    Content:     "@bot what do you think?",
    Verb:        "mention",
    ReplyTo:     parentPostID,  // if a reply
    // NO thread context, NO quoted post, NO author history
}
```

If the agent needs more context, it calls the observer's MCP tool or reads
from facts/. The adapter doesn't block on enrichment.

### 3. No observer polling in adapter

The adapter polls ONLY for notifications directed at the bot. It does NOT:

- Poll hashtags or keywords
- Poll competitor accounts
- Poll trending topics
- Crawl threads for context

All of that is the observer's job. The adapter stays thin.

## What Stays the Same

- JID format: `<platform>:user/<id>` or `<platform>:<type>/<id>`
- Outbound via `send`/`reply` MCP tools
- chanlib.RouterClient for routd communication
- Health check at `/health`
- Service token auth via AUTHD_SERVICE_NAME

## Migration

Existing social adapters (bskyd, mastd, reditd, twitd, linkd) are already
thin — they don't have observer logic. This spec documents the intended
boundary so future development doesn't conflate the two.

The observer system is new work, spec'd in `6/`. Until it ships, agents on
social platforms operate with notification-only context.

## Open Questions

1. **Triage skill standard.** Should there be a built-in `social-triage` skill,
   or is this operator-configured per deployment?

2. **Observer delivery.** MCP tool (`get_social_context`) vs facts/ files vs
   injected `<observed>` context. Each has tradeoffs.

3. **Rate limit coordination.** If both adapter and observer hit the same API,
   how do they share rate budget?

4. **Cross-platform observer.** One observer per platform, or one unified
   "social observer" that aggregates across bsky/mastodon/reddit/twitter/linkedin?

## Related

- `5/L-mention-promotion` — verb promotion for replies/reactions
- `5/G-engagement` — engagement window (applies differently to social)
- `6/` — observer system spec (TBD)
