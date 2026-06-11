---
status: draft
depends: [1-cockpit-index, 11-adapter-contract]
---

# mastd + bskyd + reditd + linkd dashboards — stream/poll social adapters

**Purpose** — instantiate the adapter contract
([`6/11`](11-adapter-contract.md)) for the four stream/poll social
adapters. Everything not listed here is the contract verbatim.

| Adapter | Transport                                                | `isConnected()` source                        | Identity           | Persisted state                       |
| ------- | -------------------------------------------------------- | --------------------------------------------- | ------------------ | ------------------------------------- |
| mastd   | user-stream websocket                                    | `streaming` flag (`mastd/client.go`)          | `me.Acct`          | none                                  |
| bskyd   | notifications poll, 10s                                  | `authed` flag (`bskyd/client.go`)             | `session.DID`      | `bluesky-session.json` in data dir    |
| reditd  | inbox + subreddit poll, `REDDIT_POLL_INTERVAL` (5m)      | last successful poll < 15m (`pollStaleAfter`) | `cfg.Username`     | `cursors.json` in data dir            |
| linkd   | own-shares comment poll, `LINKEDIN_POLL_INTERVAL` (300s) | `authed` flag (`linkd/client.go`)             | `meURN` + `meName` | `linkd-state-<name>.json` in data dir |

## mastd deltas

Show: stream state + the reconnect-backoff policy as a fact line
(exponential 1s→60s, resets on success — `mastd/client.go stream()`).
Static access token (`MASTODON_ACCESS_TOKEN`) — no refresh path, so no
auth section beyond identity. Stale threshold: chanlib default 5m.

Control: **reconnect** — contract `POST /v1/reconnect`: cancel the
stream context so the loop redials immediately (backoff reset). New
verb. No `auth/refresh` (static token), no session reset (no
persisted session). "Reload follows" — no such mechanism in code;
absent.

## bskyd deltas

Show: DID, JWT session state (access/refresh present, persisted
session file mtime). Poll cadence 10s — no per-poll cursor; staleness
is the chanlib default 5m.

Control: **refresh auth** — contract `POST /v1/auth/refresh`: run the
existing `refreshSession` → `createSession` fallback now, flip
`authed`. New verb wrapping existing functions. **reconnect** — not
separate: the poll loop never stops; refresh-auth is the recovery
verb. No session reset offered initially (deleting
`bluesky-session.json` just forces `createSession` from env creds —
refresh-auth already covers it).

## reditd deltas

Show:

- Cursor table (extra section): per-source cursor state surfaced via
  reditd's `/v1/status` (Required work below — never a direct
  `cursors.json` read from a dash handler, `6/1` read-path) — `inbox`
  - one `sr:<subreddit>` row each, with the configured subreddit list
    (env-fixed; changing it is a restart, not a control).
- Last successful poll time vs the 15m `pollStaleAfter` window (this
  IS `isConnected()` for reditd — render the same computation, don't
  re-derive).
- Rate-limit section: active when a 429 `Retry-After` sleep is in
  progress (≤5m cap, 3 attempts — `doWithRetry`); token expiry time.
- Stale threshold: 60m (`chanlib.staleThresholds["reddit"]` — sparse
  subreddits are quiet for hours).

Control: **reconnect** — contract `POST /v1/reconnect`: trigger an
immediate `pollOnce` (out-of-cycle poll). **refresh auth** — contract
`POST /v1/auth/refresh`: run the existing `refreshToken` now. Both
new verbs wrapping existing functions.

## linkd deltas

Show: `meURN` + `meName`, OAuth token expiry, last poll time.
**Staleness caveat rendered as a fact**: linkd stubs `lastInboundAt`
to `time.Now()` (`linkd/server.go` — stub adapter, no inbound
plumbing), so `stale` can never fire; the page says so instead of
showing a meaningless freshness number.

Control: **refresh auth** — contract `POST /v1/auth/refresh`: run the
existing `refreshAccessToken` now, flip `authed`. New verb.
**reconnect** — immediate `pollOnce`, same shape as reditd. New verb.

## Required `/v1` work

Beyond the shared chanlib work in `6/11`:

| Verb / field                         | Adapter              | Wraps                                       |
| ------------------------------------ | -------------------- | ------------------------------------------- |
| `POST /v1/reconnect`                 | mastd                | cancel stream ctx → immediate redial        |
| `POST /v1/auth/refresh`              | bskyd                | `refreshSession` → `createSession` fallback |
| `POST /v1/reconnect`                 | reditd               | immediate `pollOnce`                        |
| `POST /v1/auth/refresh`              | reditd               | `refreshToken`                              |
| `POST /v1/reconnect`                 | linkd                | immediate `pollOnce`                        |
| `POST /v1/auth/refresh`              | linkd                | `refreshAccessToken`                        |
| cursors + subreddits in `/v1/status` | reditd               | in-memory cursor map + configured sources   |
| token expiry in `/v1/status`         | reditd, linkd, bskyd | in-memory `expiresAt` / session             |

## Auth / HTMX / non-goals

Per [`6/11`](11-adapter-contract.md) and [`6/1`](1-cockpit-index.md).
Extra fragment: reditd `GET /dash/reditd/x/cursors`.

Non-goal additions: no cursor editing or rewind (replay risk; cursors
advance only on successful delivery by design); no follow/subreddit
management (env-config, restart to change); no posting UI (the agent
posts; operators read routd).

## Acceptance

- Contract acceptance (`6/11`) passes for all four.
- mastd: reconnect from the page redials the stream within one backoff
  cycle; status returns to `ok` on the next inbound.
- reditd: cursor table matches the cursor state `/v1/status` reports
  (the dash handler never opens `cursors.json`); reconnect
  causes a poll visible as an updated last-poll time; a 429 in
  progress shows the Retry-After countdown.
- bskyd/linkd: auth-refresh updates token expiry on the page; a
  failed refresh flips status to `disconnected`.
- linkd: the page carries the stub-inbound caveat; no stale state is
  ever rendered.
