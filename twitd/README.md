# twitd

X / Twitter channel adapter (TypeScript, Bun, agent-twitter-client).

## Purpose

Bridges X (Twitter) to the router via browser emulation — there is no
usable free official API. Wraps `agent-twitter-client` (the ai16z fork
of `@the-convocation/twitter-scraper`).

## Capability matrix

| Verb        | Status   | Notes                                                          |
| ----------- | -------- | -------------------------------------------------------------- |
| `send`      | native   | DM via `sendDirectMessage(convId, text)`                       |
| `post`      | native   | tweet to authenticated user's timeline                         |
| `reply`     | native   | `sendTweet(text, replyToId)`                                   |
| `repost`    | native   | retweet                                                        |
| `quote`     | native   | quote-tweet (text required; empty body returns hint to repost) |
| `like`      | native   | favorite (no emoji — X likes are binary)                       |
| `delete`    | native   | own tweets                                                     |
| `send_file` | native   | image/video for tweets; DMs degrade to text-only               |
| `forward`   | 501 hint | redirects to `send` with quoted text + permalink               |
| `dislike`   | 501 hint | X has no downvote; suggests `reply`                            |
| `edit`      | 501 hint | Premium-only and not exposed; suggests `delete` + new `post`   |

## JID format

| Surface                              | JID                      |
| ------------------------------------ | ------------------------ |
| User timeline (mentions arrive here) | `x:home`                 |
| Specific tweet (reply / like target) | `x:tweet/<tweet_id>`     |
| DM conversation                      | `x:dm/<conversation_id>` |
| User profile                         | `x:user/<username>`      |

## Auth (3 paths, priority order)

1. **Cookie file** at `$TWITTER_AUTH_DIR/cookies.json`. Operator imports
   a browser-exported session JSON. Recommended.
2. **Username + password** via `TWITTER_USERNAME`, `TWITTER_PASSWORD`,
   `TWITTER_EMAIL`, `TWITTER_2FA_SECRET`. Only used if cookies are
   missing or the session is invalid. On success cookies are persisted.
3. **Pair-mode CLI**: `bun dist/main.js --pair` performs login with the
   env-var creds, persists cookies, exits.

Cookies are atomically rotated to `cookies.json.bak` on every save.

## Environment

| Var                     | Purpose                                                        |
| ----------------------- | -------------------------------------------------------------- |
| `TWITTER_AUTH_DIR`      | cookie + cursor dir (default `$DATA_DIR/store/twitter-auth`)   |
| `TWITTER_USERNAME`      | login (only if cookies missing/invalid)                        |
| `TWITTER_PASSWORD`      | login                                                          |
| `TWITTER_EMAIL`         | login disambiguation                                           |
| `TWITTER_2FA_SECRET`    | TOTP secret for 2FA accounts                                   |
| `TWITTER_POLL_INTERVAL` | inbound polling cadence in seconds (default 90)                |
| `ROUTER_URL`            | gated router URL                                               |
| `CHANNEL_SECRET`        | shared HMAC secret                                             |
| `LISTEN_ADDR`           | HTTP listen, default `:8080`                                   |
| `LISTEN_URL`            | URL the router uses to call back (default `http://twitd:8080`) |

## Health

`GET /health` returns 503 with `{status:"disconnected"}` while the
session is unauthenticated, and 503 `{status:"stale"}` if no inbound
has flowed in 5 minutes. Reasonable triage targets:

- Cookies expired → operator drops new `cookies.json`, restart twitd.
- 2FA challenge during password login → switch to cookie path.

## Account-loss runbook

X suspensions are common. To swap an account:

1. Provision a backup account in advance (warm it: a few human-driven
   tweets, follow some accounts, age it ~weeks).
2. Replace credentials: drop a new `cookies.json` into
   `$TWITTER_AUTH_DIR`, or update `TWITTER_USERNAME` / `TWITTER_PASSWORD`
   in the instance `.env`.
3. Restart twitd:
   `sudo docker compose -f /srv/data/arizuko_<inst>/docker-compose.yml restart twitd`
4. Update mention-trigger filters and `@handle` references in agent
   skills if the new handle differs.

## Limitations

- No streaming. The library has no reliable streaming hook; web surface
  doesn't expose one. We poll mentions on `TWITTER_POLL_INTERVAL`.
- No `fetch_history`. Gateway's local-DB cache covers history queries.
- DM media uploads are degraded to text-only — `agent-twitter-client`
  doesn't expose DM attachment upload cleanly.
- Long-form posts (X Premium articles) are out of scope.

## Library

`agent-twitter-client@0.0.18` — solo-maintained fork; pin in
`package.json` so X-side breakage doesn't ride in transitively.
Periodic outages are expected; replan the version on incident.

## Files

- `src/main.ts` — entry, env, lifecycle, polling loop
- `src/twitter.ts` — Scraper interface, cookie + cursor persistence,
  3-path auth
- `src/server.ts` — HTTP routes (verbs + `/health`)
- `src/client.ts` — RouterClient (registration + inbound delivery)
- `src/verbs.ts` — outbound primitives + JID parsing

## Testing

```sh
cd twitd && bun test
```

42 tests cover server routes, hint behavior, error paths, JID parsing,
cookie persistence, and the 3-path auth flow. The Scraper is stubbed
in tests; we never exercise the real X surface in CI.

## Related docs

- `specs/2/k-twitter-adapter.md`
- `specs/4/1-channel-protocol.md`
