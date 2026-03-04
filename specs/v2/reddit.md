# Reddit channel (v1) — speculative

Reddit DMs (private messages) as inbound/outbound channel.
Subreddit comment monitoring as optional source (v2).
Polling via snoowrap. Free under 100 req/min.

## Source and sink

- **Inbound**: poll `r/me/messages` inbox, process unread, mark read
- **Outbound**: `redditor.message()` for DM replies
- Enabled by: `REDDIT_CLIENT_ID` + `REDDIT_CLIENT_SECRET` + credentials in .env

## JID format

`reddit:u_{username}` for DMs.
`reddit:r_{subreddit}` for subreddit threads (v2).

## OAuth / app registration

App type: `script` (self-use, no user OAuth flow needed).
Register at reddit.com/prefs/apps → script app → get client_id + secret.
Use bot account credentials (username + password) directly in config.

**Approval required** since 2025 — submit via Reddit developer portal.
Personal/hobby use is rarely approved for new apps; use an established
developer account. Once approved, free within rate limits.

**Karma / age requirement**: many subreddits require 10-100 post karma
and 2+ week old account. New accounts hit spam filters. Document
requirement; fail gracefully (log warning, skip subreddit).

## Libraries

- `snoowrap` — full Reddit API wrapper (Promise-based)
- `snoostorm` — event-based streaming on top of snoowrap (v2, subreddits)

## Rate limits

100 requests / minute (free). Snoowrap handles backoff automatically.
Poll inbox every 15s (4 req/min, well within limit).

## Config

```env
REDDIT_CLIENT_ID=...
REDDIT_CLIENT_SECRET=...
REDDIT_USERNAME=bot_account
REDDIT_PASSWORD=...
REDDIT_POLL_INTERVAL_MS=15000
```

## V1 scope

- DMs only (no subreddit monitoring)
- Poll inbox every 15s, mark messages read after processing
- No karma/age check in code — documented requirement only
- One bot account per instance

## V2 additions

- Subreddit comment monitoring via snoostorm (configurable list)
- Relevance scoring before routing to agent
- JID: `reddit:r_{subreddit}` for subreddit threads
- Config: `REDDIT_SUBREDDITS=r/claudeai,r/LocalLLaMA`
