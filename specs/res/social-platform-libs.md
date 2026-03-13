---
status: reference
---

# Social Platform Libraries — Node.js/TypeScript

Research date: 2026-03-10

## TL;DR

Mastodon and Bluesky have healthy, free, well-typed ecosystems (masto.js
and @atproto/api respectively). Twitter/X is expensive and hostile to
developers — $200/mo minimum for read access. Reddit killed self-service
API keys in late 2024, requiring pre-approval. Facebook requires App
Review for anything beyond basic profile. For a kanipi channel, Mastodon
and Bluesky are the easiest wins; Reddit and X carry significant access
barriers; Facebook is bureaucratically heavy.

## Quick Comparison

| Platform  | Best Library                   | Stars  | Last Publish | Auth               | Free Tier    | Real-Time       |
| --------- | ------------------------------ | ------ | ------------ | ------------------ | ------------ | --------------- |
| Mastodon  | masto.js (`masto`)             | 774    | 2026-03-04   | OAuth2 + token     | Yes, full    | WebSocket/SSE   |
| Bluesky   | `@atproto/api`                 | 9.2k\* | 2026-03-02   | App password/OAuth | Yes, full    | Jetstream (WS)  |
| Reddit    | raw fetch (snoowrap dead)      | —      | —            | OAuth2             | Yes, limited | No native       |
| Twitter/X | `twitter-api-v2`               | 1.5k   | 2025-11-15   | OAuth 1.0a/2.0     | Write-only   | Filtered stream |
| Facebook  | `facebook-nodejs-business-sdk` | 580    | 2025-12      | OAuth2 + review    | Yes, limited | Webhooks        |

\*Stars for the monorepo (bluesky-social/atproto), not the package alone.

---

## 1. Mastodon

### Libraries

**masto.js** (`masto` on npm) — recommended

- GitHub: https://github.com/neet/masto.js — 774 stars
- Version: 7.10.2 (2026-03-04)
- 6kB minified+gzipped, TypeScript-native, 100% test coverage
- Used by Elk and Phanpy (major Mastodon web clients)
- Supports Node.js, browsers, Deno

**megalodon** — alternative (multi-fediverse)

- GitHub: https://github.com/h3poteto/megalodon — 278 stars
- Version: 10.2.4 (2026-02-25)
- Unified API for Mastodon, Pleroma, Friendica, Firefish, GoToSocial,
  Akkoma, Sharkey, Hometown, Iceshrimp
- Heavier than masto.js but covers the entire fediverse

### Auth

OAuth2 flow. Most bots use app tokens (createApp + obtainToken).
No paid tier — Mastodon is open-source and federated.

### API Capabilities

- Post (create status): yes
- Reply: yes (in_reply_to_id)
- React/favourite: yes
- Boost (repost): yes
- Follow/unfollow: yes
- Delete: yes
- Edit: yes (since Mastodon 3.5)
- Moderation: yes (report, mute, block)
- Media upload: yes (images, video, audio)
- Polls: yes

### Real-Time

WebSocket streaming (preferred) and Server-Sent Events:

- `user` stream: own notifications, home timeline
- `public` stream: federated/local timeline
- `hashtag` stream: specific hashtag
- `list` stream: specific list
- `direct` stream: direct messages

Both masto.js and megalodon wrap the streaming API.

### Rate Limits

- 300 requests per 5 minutes (all endpoints)
- POST /api/v1/media: 30 per 30 minutes
- Account creation: 5 per 30 minutes
- Instance-specific — admins can adjust

### Gotchas

- Rate limits are per-instance, not global
- Different Mastodon versions support different API features
- Some instances run forks (Glitch, Hometown) with extended APIs
- megalodon abstracts fediverse differences but may lag behind
  latest Mastodon API additions

---

## 2. Bluesky (AT Protocol)

### Library

**@atproto/api** — official SDK

- GitHub: https://github.com/bluesky-social/atproto — 9.2k stars (monorepo)
- Version: 0.19.0 (2026-03-02)
- Official, maintained by Bluesky PBC
- TypeScript-native, works on Node.js, browsers, React Native
- 154 dependents on npm

### Auth

Two modes:

1. **App password** — simple: `agent.login({ identifier, password })`
   - Good for bots, scripts
   - Access JWT expires in minutes, refresh JWT lasts longer
2. **OAuth2** — proper: `@atproto/oauth-client-node`
   - For multi-user apps
   - DPoP-bound tokens

**Critical gotcha**: refresh token changes on every refresh call.
You must persist the new refresh token each time. If you cache the
old one, auth breaks silently. The SDK's `Agent` class handles
auto-refresh but you need a `sessionPersistHandler` callback to
save updated tokens.

### API Capabilities

- Post (create record): yes
- Reply: yes (requires parent + root refs)
- Like: yes (create like record)
- Repost: yes (create repost record)
- Follow: yes (create follow record)
- Delete: yes (delete record by rkey)
- Edit: not natively (delete + repost pattern)
- Quote post: yes
- Media upload: yes (uploadBlob)
- Custom feeds: yes (feed generators)
- Moderation: yes (report, mute, block, labeling)

### Real-Time

**Jetstream** — recommended for most use cases

- WebSocket-based, JSON events
- ~850 MB/day vs ~50 GB/day raw firehose
- Filter by collection (posts, likes, follows) and DID
- 4 public instances operated by Bluesky
- Self-hostable (Go binary)
- Connect: `wss://jetstream1.us-east.bsky.network/subscribe`

**Raw firehose** — for full network sync

- CBOR-encoded MST blocks (complex)
- Use `@atproto/sync` or the new Tap system

**@atproto/tap** — newer alternative (2025)

- Simplified repo synchronization
- TypeScript client library available

### Rate Limits

- 3000 points per 5 minutes (point cost varies by endpoint)
- createRecord (post): 3 points
- Generous for bots — roughly 1000 posts per 5 min
- No paid tier, all access is free

### Gotchas

- Reply threading requires both parent and root post references
  (not just parent) — common source of broken threads
- Record keys (rkeys) are TIDs, not sequential IDs
- The API is Lexicon-based (schema language), not REST in the
  traditional sense — endpoints are RPC-style
- Rich text (mentions, links) requires facets with byte offsets,
  not character offsets — use the SDK's RichText helper
- No edit — only delete and repost
- Token refresh gotcha described above

---

## 3. Reddit

### Libraries

**snoowrap** — archived (2024-03-17), do not use for new projects

- GitHub: https://github.com/not-an-aardvark/snoowrap — 1k stars
- Last version: 1.23.0 (2021-05-15)
- Read-only archive, 80 open issues
- Still technically works if you have existing OAuth credentials

**reddit-client-api** — small TypeScript wrapper

- GitHub: https://github.com/jamiegood/reddit-client-api
- Minimal adoption

**reddit/node-api-client** — official but abandoned

- GitHub: https://github.com/reddit/node-api-client
- Not maintained

**Recommendation: raw fetch with typed wrappers**

- Reddit's API is straightforward REST + OAuth2
- Build a thin typed client (~200 lines) around fetch
- Endpoints: `oauth.reddit.com/api/v1/...`
- This avoids depending on dead libraries

### Auth

OAuth2 with three grant types:

- **Script app** (personal use): client_credentials with username/password
- **Web app**: authorization_code flow
- **Installed app**: implicit grant

**CRITICAL (2025 change)**: Reddit killed self-service API key creation
in late 2024. All new OAuth apps require pre-approval. You must submit
a request describing your use case, target subreddits, expected volume.
~7 day review turnaround. Existing credentials still work.

### API Capabilities

- Post (submit): yes (link or self-post)
- Reply (comment): yes
- Vote (upvote/downvote): yes (not "react")
- No repost equivalent
- Follow (subscribe to subreddit): yes
- Delete: yes (own content)
- Edit: yes (own content)
- Moderation: yes (if moderator — remove, approve, ban, flair)
- Media: yes (upload via media endpoint)

### Real-Time

No native real-time API. Options:

- Poll endpoints (respect rate limits)
- Reddit's Push Notification system is mobile-only
- No WebSocket/SSE/streaming endpoint for third parties

### Rate Limits

- 100 requests per minute (OAuth, free tier)
- 10-minute rolling window tracking
- Must send User-Agent header (Reddit throttles/blocks without it)
- No paid API tier (Reddit monetizes through Devvit platform instead)

### Gotchas

- Pre-approval requirement is the biggest barrier — plan ahead
- Rate limit headers: `X-Ratelimit-Remaining`, `X-Ratelimit-Reset`
- Reddit's API returns 429 aggressively
- Some endpoints require specific OAuth scopes
- `.json` suffix on any reddit URL returns JSON (useful for read-only)
- Old reddit API (v1) still works; there is no v2

---

## 4. Twitter/X

### Library

**twitter-api-v2** — best available

- GitHub: https://github.com/PLhery/node-twitter-api-v2 — 1.5k stars
- Version: 1.28.0 (2025-11-15)
- TypeScript-native, zero dependencies, 23kB
- Supports v1.1 and v2 APIs
- Auto-reconnect streaming, chunked media upload, pagination
- Plugins: token refresher, rate limiting, Redis cache
- Maintainer continues despite frustration with X's direction

### Auth

- OAuth 1.0a (user context — legacy but still works)
- OAuth 2.0 with PKCE (user context — recommended)
- OAuth 2.0 Bearer Token (app-only context)
- API Key + Secret (for OAuth 1.0a signing)

### API Capabilities (depends on tier)

| Action      | Free  | Basic ($200/mo) | Pro ($5k/mo) |
| ----------- | ----- | --------------- | ------------ |
| Post tweet  | yes   | yes             | yes          |
| Read tweets | no    | yes             | yes          |
| Reply       | yes\* | yes             | yes          |
| Like        | no    | yes             | yes          |
| Retweet     | no    | yes             | yes          |
| Follow      | no    | yes             | yes          |
| Delete      | yes   | yes             | yes          |
| Edit        | no    | limited         | yes          |
| DMs         | no    | no              | yes          |
| Search      | no    | yes (limited)   | yes (full)   |

\*Reply requires knowing the tweet ID, which requires read access.

### Pricing (as of 2026)

- **Free**: write-only, ~500 posts/month, `GET /2/users/me` only
- **Basic**: $200/month, 100 reads/month cap, most v2 endpoints
- **Pro**: $5,000/month, 1M tweets, full archive search
- **Enterprise**: $42,000+/month, custom
- **Pay-as-you-go**: launched Feb 2026, per-request pricing (beta)

### Real-Time

- Filtered stream (v2): real-time tweets matching rules
  - Free: not available
  - Basic: 1 connection, 5 rules
  - Pro: 2 connections, 1000 rules
- Volume stream: sampled 1% of all tweets (Enterprise only)
- Webhooks: Account Activity API (deprecated for most tiers)

### Rate Limits

- Free: 24-hour windows, very restrictive
  - POST tweets: 50/day (some sources say 1500/month)
- Basic: 15-minute windows
  - POST tweets: 100/15min
  - GET tweets: varies by endpoint
- All tiers have monthly post consumption caps

### Gotchas

- Free tier is nearly useless — can post but can't read
- $200/mo Basic is the real minimum for a functional bot
- API changes frequently with little notice
- v1.1 endpoints being gradually deprecated
- Media upload still requires v1.1 endpoint
- Tweet IDs are snowflakes (BigInt — use string in JS)
- Rate limit headers inconsistent across endpoints
- Pay-as-you-go pricing still in beta, unclear long-term

---

## 5. Facebook/Meta (Graph API)

### Library

**facebook-nodejs-business-sdk** — official

- GitHub: https://github.com/facebook/facebook-nodejs-business-sdk — 580 stars
- Version: 22.0.2 (late 2025)
- Official Meta SDK, auto-generated from API schema
- Covers Marketing API + Graph API (Pages, Groups, Instagram)
- CommonJS + ESM + browser builds

**Alternatives**:

- `fbgraph` — lightweight Graph API wrapper (old, minimal maintenance)
- Raw `fetch` — Graph API is straightforward REST, often simpler
  than the bloated official SDK

### Auth

- OAuth2 with Facebook Login
- Page Access Tokens (long-lived, via token exchange)
- App Access Tokens (server-to-server)
- System User Tokens (Business Manager)

**App Review required** for most permissions beyond basic profile.
This is a manual review process where Meta evaluates your app's
use case, privacy policy, data handling. Can take weeks.

### API Capabilities

| Action          | Permission Required                        |
| --------------- | ------------------------------------------ |
| Post to Page    | pages_manage_posts, pages_read_engagement  |
| Post to Group   | publish_to_groups (app installed in group) |
| Comment         | pages_manage_engagement                    |
| React           | pages_manage_engagement                    |
| Delete post     | pages_manage_posts                         |
| Edit post       | pages_manage_posts                         |
| Read Page feed  | pages_read_engagement                      |
| Read Group feed | groups_access_member_info (restricted)     |
| Moderation      | pages_manage_metadata                      |
| Media upload    | pages_manage_posts                         |

### Real-Time

- **Webhooks**: primary real-time mechanism
  - Page updates, feed changes, messages, comments
  - Requires HTTPS callback URL with verification
  - Reliable but requires public endpoint
- No WebSocket/SSE
- Polling: allowed but rate-limited

### Rate Limits

- App-level: 200 calls per hour per user
- Page-level: varies, not publicly documented exactly
- Platform Rate Limiting: Meta throttles based on overall app
  quality score and user complaint rate
- Marketing API has separate, stricter limits

### Gotchas

- **App Review is the main barrier** — requires privacy policy,
  terms of service, screencast demo, detailed description
- Group API access heavily restricted after Cambridge Analytica
- Personal profile posting via API was removed entirely
- Token expiration: short-lived tokens last 1 hour, long-lived
  last 60 days, page tokens can be made permanent
- Graph API versions deprecate on ~2-year cycle — must upgrade
- The official SDK is bloated (designed for ads/marketing) —
  raw fetch is often simpler for page/group operations
- Instagram posting requires separate permissions and business
  account linkage

---

## Recommendation for kanipi Channels

**Tier 1 — implement first (free, good APIs):**

1. **Mastodon** via masto.js — federated, free, excellent streaming,
   well-typed. Most aligned with kanipi's open philosophy.
2. **Bluesky** via @atproto/api — free, growing fast, Jetstream
   for real-time. Official SDK is excellent.

**Tier 2 — implement if needed (access barriers):** 3. **Reddit** via raw fetch — pre-approval required but API is
simple once you have credentials. No real-time.

**Tier 3 — implement only if required (paid/bureaucratic):** 4. **Twitter/X** via twitter-api-v2 — $200/mo minimum for
anything useful. Good library, hostile platform. 5. **Facebook** via raw fetch (not the bloated SDK) — App Review
required, restrictive permissions, enterprise-oriented.

## Sources

### Mastodon

- https://github.com/neet/masto.js
- https://github.com/h3poteto/megalodon
- https://docs.joinmastodon.org/methods/streaming/
- https://docs.joinmastodon.org/api/rate-limits/

### Bluesky

- https://github.com/bluesky-social/atproto
- https://docs.bsky.app/docs/get-started
- https://docs.bsky.app/blog/jetstream
- https://docs.bsky.app/blog/oauth-atproto
- https://docs.bsky.app/blog/api-v0-14-0-release-notes
- https://github.com/bluesky-social/atproto/issues/3637

### Reddit

- https://github.com/not-an-aardvark/snoowrap
- https://support.reddithelp.com/hc/en-us/articles/16160319875092
- https://replydaddy.com/blog/reddit-api-pre-approval-2025-personal-projects-crackdown
- https://www.wappkit.com/blog/reddit-api-credentials-guide-2025

### Twitter/X

- https://github.com/PLhery/node-twitter-api-v2
- https://getlate.dev/blog/twitter-api-pricing
- https://docs.x.com/x-api/fundamentals/rate-limits
- https://elfsight.com/blog/how-to-get-x-twitter-api-key-in-2026/

### Facebook/Meta

- https://github.com/facebook/facebook-nodejs-business-sdk
- https://developers.facebook.com/docs/graph-api
- https://developers.facebook.com/docs/graph-api/overview/rate-limiting/
