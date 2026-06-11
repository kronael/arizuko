---
status: draft
depends: [1-cockpit-index, 11-adapter-contract]
---

# discd + emaid + twitd dashboards — mixed gateway adapters

**Purpose** — instantiate the adapter contract
([`6/11`](11-adapter-contract.md)) for the three mixed gateway
adapters. Everything not listed here is the contract verbatim.

| Adapter | Transport                                                       | `isConnected()` source                                 | Identity                       | Persisted state                                   |
| ------- | --------------------------------------------------------------- | ------------------------------------------------------ | ------------------------------ | ------------------------------------------------- |
| discd   | Discord gateway websocket (discordgo)                           | `session != nil && session.DataReady` (`discd/bot.go`) | `session.State.User` (name+ID) | none                                              |
| emaid   | IMAP IDLE (28m keepalive) + 30s poll fallback; SMTP per-message | `connected` flag (`emaid/imap.go`)                     | `EMAIL_ACCOUNT`                | `emaid.db` (thread tables) in data dir            |
| twitd   | mentions poll, `TWITTER_POLL_INTERVAL` (90s)                    | `connected` flag (`twitd/src/main.ts`)                 | cookie session (handle)        | `$TWITTER_AUTH_DIR/cookies.json` + `cursors.json` |

## discd deltas

Show:

- Gateway state (`DataReady`), bot user, token mode (bot vs user —
  `DISCORD_BOT_TOKEN` / `DISCORD_USER_TOKEN`, shown as a fact, never
  the token).
- Guild/channel cache **counts** from discordgo `session.State` —
  read-only. The cache is event-maintained by discordgo; there is no
  refresh API in use, so no refresh-cache control (absent, per `6/11`).
- Rate-limit section: send retry policy (3 attempts, 1s linear, typed
  `discordgo.RateLimitError` detection — `discd/bot.go isRateLimit`).

Control: **reconnect** — contract `POST /v1/reconnect`:
`session.Close()` + `session.Open()`. New verb. No auth refresh
(permanent token), no session reset (no persisted session).

## emaid deltas

Show:

- Active inbound mode: `idle` vs `poll` fallback, plus the
  reconnect-backoff fact (exponential ≤60s — `emaid/imap.go`).
- Folder: INBOX only (hardcoded). emaid has no multi-folder support,
  so "resync folders" has no mechanism — absent.
- SMTP is per-message dial (no persistent link); last outbound comes
  from the contract's shared `lastOutboundAt`, which doubles as the
  SMTP health signal.
- Thread store: row counts of `email_threads` / `email_msg_ids` from
  `emaid.db` (emaid owns this DB; reading it in-process is the
  owning-daemon read path, not a cross-daemon DB reach).
- Stale threshold: 10m (`chanlib.staleThresholds["email"]`).

Control: **reconnect** — contract `POST /v1/reconnect`: close the
IMAP client so the retry loop redials immediately (backoff reset).
New verb. No auth refresh (static IMAP/SMTP password), no session
reset.

## twitd deltas

Show:

- Cookie session state: cookies file present + mtime, login validity
  (last `isLoggedIn` result), poll interval, per-source cursors from
  `cursors.json` (mentions; dms/likes/retweets/followers as present).
- Health note: twitd serves its own `/health` (TS); 5m staleness is a
  **503** like whapd.

Control:

- **Refresh auth** — contract `POST /v1/auth/refresh`: re-run the
  existing login flow (validate cookies → password login from
  `TWITTER_USERNAME`/`TWITTER_PASSWORD`/`TWITTER_EMAIL`/2FA → persist
  cookies). New verb wrapping the boot-time flow.
- **Reset session** — contract `session-reset`: delete `cookies.json`
  (cursors untouched), forcing a fresh password login on the next
  refresh/restart. `.btn-danger`, confirm. New verb.
- **Reconnect** — immediate out-of-cycle `pollMentions`. New verb.

TS: twitd implements the gate + vendored theme per `6/11` "TS
adapters".

## Required `/v1` work

Beyond the shared chanlib work in `6/11`:

| Verb / field                           | Adapter | Wraps                                      |
| -------------------------------------- | ------- | ------------------------------------------ |
| `POST /v1/reconnect`                   | discd   | `session.Close()` + `Open()`               |
| cache counts in `/v1/status`           | discd   | discordgo `session.State` guilds/channels  |
| `POST /v1/reconnect`                   | emaid   | close IMAP client → retry loop redials     |
| mode + thread counts in `/v1/status`   | emaid   | idle/poll flag + `emaid.db` row counts     |
| `POST /v1/auth/refresh`                | twitd   | cookie validate → password login → persist |
| `POST /v1/session/reset`               | twitd   | delete `cookies.json`                      |
| `POST /v1/reconnect`                   | twitd   | immediate `pollMentions`                   |
| cursors + cookie state in `/v1/status` | twitd   | `cursors.json` + cookies file state        |

## Auth / HTMX / non-goals

Per [`6/11`](11-adapter-contract.md) and [`6/1`](1-cockpit-index.md).
No extra fragments beyond the contract set (cursor/cache data rides
`x/status`).

Non-goal additions: no guild/channel browsing or Discord admin; no
mailbox browsing (content lives in routd; emaid's `/files/` part
fetch stays an agent path); no cursor editing (same replay rule as
`6/13`); no Twitter posting UI.

## Acceptance

- Contract acceptance (`6/11`) passes for all three.
- discd: reconnect drops and re-opens the gateway; `DataReady` flips
  false → true on the page; cache counts repopulate.
- emaid: page shows the active mode; reconnect from poll-fallback
  mode re-attempts IDLE; a send updates last-outbound.
- twitd: auth-refresh after expired cookies restores `ok`;
  session-reset removes `cookies.json`, page shows `disconnected`
  with login invalid until refresh succeeds; cursors only ever
  advance.
