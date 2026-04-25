---
status: planned
phase: next
---

# twitd — X/Twitter Adapter via Browser-Emulation

X has no usable free API. `twitd` ships a TypeScript daemon that drives
the X web surface through `agent-twitter-client` (ai16z fork of
`@the-convocation/twitter-scraper`). Same library ElizaOS uses; pin
to latest stable at ship time.

No official Twitter API. Cookies are the unit of auth.

## Architecture

Mirrors `whapd`: Bun runtime, TypeScript, HTTP server registers with
the router, polls for inbound, exposes outbound endpoints.

```
twitd/
  Dockerfile
  package.json
  tsconfig.json
  src/
    main.ts         entrypoint, env, lifecycle
    twitter.ts      agent-twitter-client wrapper, auth, cursors
    server.ts       HTTP: /health, /send, /post, /reply, ...
    client.ts       router registration, outbound to gateway
```

Compose template: `template/services/twitd.toml`. Adapter caps key
added to `compose/compose.go:daemonKeys`.

## Auth

Three paths, priority order:

1. **Cookie file** — `/srv/data/store/twitter-auth/cookies.json`,
   operator imports a browser-exported session JSON. No login flow,
   no captcha, no 2FA risk. Recommended.
2. **Username + password** — `TWITTER_USERNAME`, `TWITTER_PASSWORD`,
   `TWITTER_EMAIL`, `TWITTER_2FA_SECRET` (TOTP). Daemon logs in on
   startup, persists cookies. Risk: 2FA challenge, captcha,
   "unusual activity" prompts can fail headless.
3. **Pair-mode CLI** — `arizuko pair <inst> twitd` runs interactive
   login, captures cookies, exits. Same shape as whapd's QR pairing.

Cookie persistence: `/srv/data/store/twitter-auth/cookies.json` plus
`cookies.json.bak` (mirror whapd's `whatsapp-auth/creds.json`
rotation). Atomic write via temp + rename.

## JID format

Prefix is `x:` (matches platform's current name; shorter than
`twitter:`).

| Surface                              | JID                      |
| ------------------------------------ | ------------------------ |
| User timeline (mentions arrive here) | `x:home`                 |
| Specific tweet (reply / like target) | `x:tweet/<tweet_id>`     |
| DM conversation                      | `x:dm/<conversation_id>` |
| User profile                         | `x:user/<username>`      |

## Verbs

| Verb        | Native | Library method                         | Notes                        |
| ----------- | ------ | -------------------------------------- | ---------------------------- |
| `send`      | yes    | `sendDirectMessage(convId, text)`      | DMs only                     |
| `post`      | yes    | `sendTweet(text, ...)`                 | Tweet to feed                |
| `reply`     | yes    | `sendTweet(text, replyToId)`           | Threaded                     |
| `repost`    | yes    | `retweet(id)`                          |                              |
| `quote`     | yes    | `sendQuoteTweet(text, quoteId)`        |                              |
| `like`      | yes    | `likeTweet(id)`                        |                              |
| `dislike`   | hint   | n/a                                    | X has no downvote            |
| `delete`    | yes    | `deleteTweet(id)`                      | Own tweets only              |
| `forward`   | hint   | n/a                                    | No DM forward primitive      |
| `edit`      | hint   | uncertain                              | Premium-only; coverage flaky |
| `send_file` | yes    | `sendTweet(text, media)` / DM w/ media | Media upload                 |

`send`, `post`, `reply`, `repost`, `quote`, `like`, `delete`,
`send_file` are registered MCP tools. `dislike`, `forward`, `edit`
are hint-only (advertised but no-op with explanatory error).

## Inbound polling

Cursor-based. Persist `last_seen_id` per source in
`/srv/data/store/twitter-auth/cursors.json`.

| Poll                       | Emits                                    |
| -------------------------- | ---------------------------------------- |
| `getMentions()`            | `reply` / `message` (depends on context) |
| `getDirectMessages()`      | `message` (DM)                           |
| New likes on own tweets    | `like` (with emoji metadata = "♥")       |
| New retweets on own tweets | `repost`                                 |
| New follows                | `follow`                                 |

Default poll interval: 60-120s, configurable via
`TWITTER_POLL_INTERVAL`. X web rate limits are stricter than the
official API — go conservative. On 429 / "rate limit" response:
exponential backoff (60s, 120s, 240s, max 600s).

## Capability advertisement

Adapter `/caps` endpoint returns the verb table above. Hint-only
verbs flagged `hint: true`. Gateway uses caps to decide whether to
register the MCP tool for agents in groups bound to this adapter.

## Account-loss recovery

X suspensions are common — ElizaOS deployments lose accounts
routinely. Operator runbook for swap:

1. Provision backup account in advance (warm it: a few human-driven
   tweets, follow some accounts, age it).
2. Replace credentials: drop new `cookies.json` into
   `/srv/data/store/twitter-auth/`, or update
   `TWITTER_USERNAME`/`TWITTER_PASSWORD` in `.env`.
3. Restart twitd: `sudo docker compose -f /srv/data/arizuko_<inst>/docker-compose.yml restart twitd`.
4. Update routes if the new account has a different handle (group
   bindings keyed by JID don't change, but mention-trigger filters
   and any `@handle` references in skills do).

## Compose & env

```
TWITTER_AUTH_DIR=/srv/data/store/twitter-auth
TWITTER_USERNAME=
TWITTER_PASSWORD=
TWITTER_EMAIL=
TWITTER_2FA_SECRET=
TWITTER_POLL_INTERVAL=90
TWITD_PORT=7080
```

`TWITTER_AUTH_DIR` mounted as volume; survives container recreation.

## Effort estimate

~1500-2000 LOC TypeScript + Dockerfile + compose template + tests.
Comparable to whapd, somewhat larger because of more verbs +
multi-source polling.

## Risks

- **Account suspension** — design around easy account swap.
- **Library churn** — `agent-twitter-client` is solo-maintained and
  breaks when X changes its web surface. Pin version, expect
  periodic outages, plan for upstream lag.
- **2FA challenges** — cookie-import bypasses entirely; password
  path may fail when X serves a verification screen.
- **Rate limits** — web throttling is independent of API limits;
  aggressive polling triggers it. Default interval is conservative.
- **No official API** — feature gaps for surfaces only the API
  exposes (Spaces metadata, advanced analytics, list management).

## Out of scope (initial ship)

- Spaces (audio rooms)
- Lists, bookmarks, polls, scheduling
- Follow / unfollow / mute / block (potential v2)
- Long-form (X premium articles)
- Twitter API hybrid mode (gated by API key presence)

## Decisions

- JID prefix is `x:`, not `twitter:` — matches the platform's
  current name and is shorter.
- Cookie file is the supported auth path; password login is a
  fallback, pair-mode is an operator convenience.
- Polling, not streaming. The library has no reliable streaming
  hook, and the web surface doesn't expose one.
- `dislike`, `forward`, `edit` advertised as hints so agents get
  consistent verb surface; runtime returns explanatory error.
