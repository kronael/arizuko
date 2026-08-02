---
status: shipped
---

# twitd — X/Twitter via browser emulation

X has no usable free API, so `twitd` drives the X web surface through
`agent-twitter-client` (the ai16z fork of
`@the-convocation/twitter-scraper`). TypeScript on Bun, mirroring
`whapd`'s shape, because the library is JS-only.

**Cookies are the unit of auth.** Three paths, in preference order:
a browser-exported `cookies.json` (no login flow, no captcha, no 2FA
risk — the supported path); username/password with optional TOTP
(fallback, and it fails when X serves a verification screen);
`arizuko pair <inst> twitd` (operator convenience, same shape as whapd's
QR pairing). Cookies persist to `TWITTER_AUTH_DIR` with a `.bak` mirror
and atomic temp+rename, matching whapd's `creds.json` rotation.

## JID prefix is `twitter:`

`twitd` registers `jid_prefixes: ['twitter:']`
(`twitd/src/client.ts`, `twitd/src/server.ts`). Surfaces are
`twitter:home` (timeline; mentions arrive here), `twitter:tweet/<id>`,
`twitter:dm/<conversation_id>`, `twitter:user/<username>` —
`twitd/src/twitter.ts` `parseJid` is canonical.

A short-lived `x:` prefix was considered to match the platform's rename
and rejected: the JID prefix is a stable wire identity, and churning it
to track branding would have broken every stored route and every
`chat_jid` already in the messages table for no functional gain.

## Verbs and hints

Native: `send` (DM), `post`, `reply`, `repost`, `quote`, `like`,
`delete`, `send_file`. Advertised but no-op with an explanatory error:
`dislike` (X has no downvote), `forward` (no DM forward primitive),
`edit` (premium-only, flaky coverage).

Hint-only verbs are advertised deliberately so agents see one consistent
verb surface across adapters and learn the fallback from the error text
rather than from a missing tool. Per-adapter verb tables live in
`twitd/README.md`; the roster is
[`../4/social-adapters.md`](../4/social-adapters.md).

## Polling, not streaming

The library exposes no reliable streaming hook and the web surface
doesn't offer one. Cursor-based polling persists `last_seen_id` per
source. X's _web_ throttling is independent of and stricter than the
official API's, so the default interval is deliberately conservative
(`TWITTER_POLL_INTERVAL`, 60–120s) with exponential backoff to 600s on 429.

## Account-loss recovery

X suspensions are routine for automated accounts, so the design target
is a cheap account swap, not suspension avoidance: warm a backup account
in advance, drop a new `cookies.json` into `TWITTER_AUTH_DIR` (or update
the credentials in `.env`), restart twitd. Group bindings are keyed by
JID and don't change; mention-trigger filters and `@handle` references
in skills do.

The library is solo-maintained and breaks when X changes its web
surface — pin the version and expect periodic outages.

## Out of scope

Spaces, lists, bookmarks, polls, scheduling, follow/unfollow/mute/block,
long-form articles, and an API hybrid mode.
