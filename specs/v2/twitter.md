# Twitter/X channel (v2)

Inbound/outbound via `agent-twitter-client` (elizaOS).
No paid API — reverse-engineered client, cookie-based auth.

## Source and sink

- **Inbound**: poll DMs + timeline on interval, track cursors
- **Outbound**: send DM, reply, repost, react, post
- Enabled by: `TWITTER_USERNAME` + `TWITTER_PASSWORD` + `TWITTER_EMAIL` in .env
- Cookies cached to `store/twitter-cookies.json` after first login

## JID format

`x:{userId}` for DMs and @mentions.
`x:timeline` for synthetic timeline inbound events.

## Library

`agent-twitter-client` (elizaOS/agent-twitter-client) — reverse-engineered
Twitter frontend GraphQL API. Supports DMs, timeline, search, post, reply, repost.

## Auth flow

1. First run: login with username + password + email → save cookies
2. Subsequent runs: load cookies, skip login
3. If cookies stale: fall back to password login, refresh cookies

## Risk

High. Twitter actively fights scrapers — breaking changes frequent (mass failures
seen Nov 2025). Use a disposable account. Do not use personal credentials.
Graceful degradation required: log warning, disable channel on repeated failures.

## Phase 1 — feed events (synthetic inbound)

Adapter polls feed and converts to IPC input messages with `event_type`:

- `dm` — direct message received
- `mention` — @mention in a tweet
- `timeline_post` — tweet from watched accounts/keywords
- `reply_to_us` — reply to one of our tweets

## Outbound action types

Outbound IPC message `type` field controls action:

- `reply` — reply to a tweet (requires `in_reply_to_id`)
- `repost` — retweet
- `react` — like
- `post` — new tweet (no reply)
- `dm` — direct message

## Config

```env
TWITTER_USERNAME=...
TWITTER_PASSWORD=...
TWITTER_EMAIL=...
TWITTER_POLL_INTERVAL_MS=90000
TWITTER_WATCH_ACCOUNTS=...    # comma-separated user IDs or handles, phase 1
TWITTER_WATCH_KEYWORDS=...    # comma-separated keywords/hashtags, phase 1
```

## V1 scope (DMs only)

- Text DMs only
- Polling at `TWITTER_POLL_INTERVAL_MS` (default 90s)
- Cookie cache with password fallback
- Graceful degradation on repeated auth failures

## V2 scope (feed)

- Timeline + mention monitoring (phase 1 feed events above)
- Keyword/account watching
- Full outbound action types
