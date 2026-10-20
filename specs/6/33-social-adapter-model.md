---
status: shipped
shipped: 2026-08-30
---

# Social Adapter Model

Social adapters (bskyd, reditd, twitd, linkd) operate differently from
chat adapters (teled, discd, slakd, whapd, emaid). This spec defines that
boundary.

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

## Design: the adapter is the notification stream

The social adapter covers the **immediate interaction landscape** only:

- Direct mentions of the bot account
- Replies to the bot's own posts
- DMs (where the platform supports them)

Reactions and likes are an OUTBOUND capability only. Each adapter declares
`like` in its capability map (`bskyd/main.go:16`, `reditd/main.go:19`,
`linkd/main.go:19`) so the agent can react, and none of them fetches one:
bskyd asks the notifications API for `reasons: "mention,reply"`
(`bskyd/client.go:228`), reditd reads the inbox and the configured subreddit
feeds, linkd reads a post's comments, twitd reads mentions and replies. A
route keyed on an inbound `verb=like` matches nothing.

This is the same event-driven model as chat adapters: platform pushes (or
adapter polls) notifications, adapter forwards to routd, agent replies via
`send`/`reply`. The adapter is thin; it doesn't crawl, doesn't observe, doesn't
index. Broad monitoring — keyword/hashtag tracking, competitor mentions,
sentiment, trend detection, thread context enrichment — is not the adapter's
job.

**Key difference from chat adapters:**

| Aspect             | Chat adapter           | Social adapter        |
| ------------------ | ---------------------- | --------------------- |
| Engagement default | Always reply           | Agent decides         |
| Context available  | Full thread in payload | Notification only     |
| Polling            | Rare (webhooks)        | Common (rate-limited) |
| Trust model        | Authenticated users    | Public strangers      |

## Adapter Behavior

### 1. Notifications are ordinary inbound

```
notification → adapter → routd (stored, verb per §"Inbound verbs") → routing rules
```

Whether the agent engages is the route's and the agent's decision, not the
adapter's. Nothing ships a triage skill: a social route defaults to the same
always-engage path a chat route takes, and the operator narrows it with the
route table (`ROUTING.md` § The route table sends a message to the first matching rule) or the persona.

### 2. Minimal context in payload

The adapter sends only what the platform's notification API provides:

```go
InboundMsg{
    ID:          notificationID,
    ChatJID:     "bsky:user/did:plc:abc123",
    Sender:      "bsky:user/did:plc:xyz789",
    SenderName:  "@alice.bsky.social",
    Content:     "@bot what do you think?",
    Verb:        "message",     // "reply" when the notification is a reply
    ReplyTo:     parentPostID,  // if a reply
    // NO thread context, NO quoted post, NO author history
}
```

The adapter doesn't block on enrichment.

### 3. Inbound verbs

Each adapter sets `InboundMsg.Verb` from the shape of the notification it
read. These four values are the whole inbound vocabulary:

| Adapter | Verbs emitted              | Set at                     |
| ------- | -------------------------- | -------------------------- |
| bskyd   | `message`, `reply`         | `bskyd/client.go:289-291`  |
| reditd  | `message`, `reply`, `post` | `reditd/client.go:353-365` |
| linkd   | `comment`, `reply`         | `linkd/client.go:468-471`  |
| twitd   | `message`, `reply`         | `twitd/src/poll.ts:56`     |

`mention` is not among them. routd assigns it centrally, and only in two
cases: the inbound replies to a bot row or lands in a thread the bot has
already spoken in (`routd/server.go:577-580`, the `5/L` promotion), or a
reaction carries a held-notice verdict (`routd/server.go:587-589`). A
first-contact @mention from a stranger satisfies neither, so it reaches the
route table with the verb its adapter set.

An operator route that must catch a first-contact mention therefore keys on
`message`, `reply`, `comment` or `post`. Keying it on `mention` or `like`
silently matches nothing — the route table reports no error for a verb no
adapter emits.

### 4. No observer polling in adapter

The adapter polls ONLY for notifications directed at the bot. It does NOT:

- Poll hashtags or keywords
- Poll competitor accounts
- Poll trending topics
- Crawl threads for context

The adapter stays thin.

## What Stays the Same

- JID format: `<platform>:user/<id>` or `<platform>:<type>/<id>`
- Outbound via `send`/`reply` MCP tools
- chanlib.RouterClient for routd communication
- Health check at `/health`
- Service token auth via AUTHD_SERVICE_NAME

## The one exception: reditd's subreddit poll (measured 2026-08-30)

Three of the four social adapters are thin. reditd is not.

`reditd.pollOnce` (`reditd/client.go:287`) polls TWO kinds of source:

```go
rc.pollSource("inbox", "/message/inbox.json", router)      // bot-directed: allowed
for _, sr := range rc.cfg.Subreddits {
    rc.pollSource("sr:"+sr, "/r/"+sr+"/new.json", router)  // a FEED: observer work
}
```

The inbox poll is exactly what this spec permits — notifications addressed to
the bot. The subreddit poll is not: `/r/<sr>/new.json` is the whole subreddit's
new-post feed, in which the bot is not addressed at all. That is "poll hashtags
or keywords" under another name, and §"No observer polling in adapter" forbids
it.

bskyd, linkd and twitd carry no equivalent: a grep for
hashtag/trending/keyword/search across them returns nothing.

**The exception is RECORDED, not removed.** Deleting reditd's subreddit poll
would take away a working capability on a deployed adapter with no
replacement. So `SUBREDDITS` stays, named here as the one adapter that crosses
the boundary, and `tests/socialboundary` pins the set so a SECOND adapter
cannot cross it quietly. That scan reads each adapter's Go package and its
`src/*.ts`, so all four are inside it and the implementation language decides
nothing.

Agents on social platforms operate with notification-only context — except on
Reddit, where the configured subreddits arrive as ordinary inbound messages.

## Related

- `5/L-mention-promotion` — verb promotion for replies/reactions
- `5/G-engagement` — engagement window (applies differently to social)
- [`6/32`](32-adapter-dashboards.md) — the operator dashboard deltas, for
  these four and the six non-social adapters beside them
