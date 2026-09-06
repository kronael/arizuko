---
status: shipped
shipped: 2026-08-30
depends: [28-cockpit-index, 31-adapter-contract]
---

# Adapter dashboards — the ten channel adapters

Instantiates the adapter contract ([`6/31`](31-adapter-contract.md))
for all ten adapters. The contract owns the page grammar, show matrix,
health semantics and auth — none of it is restated. This spec is the
per-adapter delta table plus the handful of facts that do not
generalise.

## Transport, liveness, identity, state

| Adapter | Transport                                                | `isConnected()` source                         | Identity                                               | Persisted state                                   |
| ------- | -------------------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------ | ------------------------------------------------- |
| whapd   | Baileys websocket (WhatsApp), TypeScript                 | `connection.update` flag (`whapd/src/main.ts`) | phone JID from Baileys creds                           | `$WHATSAPP_AUTH_DIR/creds.json` (+ `.bak`)        |
| teled   | Bot API long-poll, 30s `getUpdates`                      | poll success flag (`teled/bot.go`)             | `api.Self.UserName` + ID                               | offset file `$DATA_DIR/teled-offset-<channel>`    |
| slakd   | Events API webhook push (`/slack/events`)                | 60s `auth.test` probe (`slakd/bot.go`)         | `BotUserID()` + `TeamID()`                             | none                                              |
| bskyd   | notifications poll, 10s                                  | `authed` flag (`bskyd/client.go`)              | `session.DID`                                          | `bluesky-session.json`                            |
| reditd  | inbox + subreddit poll, `REDDIT_POLL_INTERVAL` (5m)      | last successful poll < 15m (`pollStaleAfter`)  | `cfg.Username`                                         | `cursors.json`                                    |
| linkd   | own-shares comment poll, `LINKEDIN_POLL_INTERVAL` (300s) | `authed` flag (`linkd/client.go`)              | `meURN` + `meName`                                     | `linkd-state-<name>.json`                         |
| discd   | Discord gateway websocket (discordgo)                    | `session.DataReady` (`discd/bot.go`)           | `session.State.User`                                   | none                                              |
| emaid   | IMAP IDLE (28m keepalive) + 30s poll fallback; SMTP dial | `connected` flag (`emaid/imap.go`)             | `EMAIL_ACCOUNT`                                        | `emaid.db` (thread tables)                        |
| twitd   | mentions poll, `TWITTER_POLL_INTERVAL` (90s), TypeScript | `connected` flag (`twitd/src/main.ts`)         | cookie session (handle)                                | `$TWITTER_AUTH_DIR/cookies.json` + `cursors.json` |
| gitd    | GitHub webhook push (`POST /github/{source...}`)         | `connected` flag, set at start (`gitd/bot.go`) | `GITHUB_TOKEN` login (`GET /user`); empty ingress-only | none of its own; sources in `routd.db`            |

## Controls and delta sections

An affordance with no underlying mechanism is **absent from the page**,
not greyed out (`6/31`).

| Adapter | Controls                    | Delta sections beyond the contract                                                  |
| ------- | --------------------------- | ----------------------------------------------------------------------------------- |
| whapd   | start pairing + pair status | pairing pane (state machine, code + 60s expiry, 5/hour bucket)                      |
| teled   | —                           | —                                                                                   |
| slakd   | —                           | —                                                                                   |
| bskyd   | —                           | the account the session is connected as (DID + handle); the JWTs are never rendered |
| reditd  | —                           | cursor table (`inbox` + one `sr:<subreddit>` row each)                              |
| linkd   | —                           | —                                                                                   |
| discd   | —                           | —                                                                                   |
| emaid   | —                           | the account and host the IMAP/SMTP login uses; the password is excluded             |
| twitd   | —                           | —                                                                                   |
| gitd    | —                           | last accepted delivery per configured source                                        |

A delta section reads the adapter's own in-memory state through
`DashSectioner`; a dash handler never opens `cursors.json`, `cookies.json`,
or `emaid.db` directly (`6/28` read-path).

Adapters with no control are stating a fact, not deferring: discd holds a
static token, so there is no auth refresh; slakd's Events API is push, so
there is no connection to re-establish; emaid's IMAP/SMTP password is
static; bskyd's session file only feeds `createSession`.

## Adapter-specific facts

**whapd — Baileys reconnection is internal.** The only meaningful
recoveries are re-pair and reset. The dashboard is the operator UI for
the **pairing-code** path, preferred over reading a QR out of container
logs (see `whapd/README.md`, the `--pair <phone>` flag); the pair rate
limit's 429 surfaces verbatim. QR rendering stays out of the dashboard.

**teled — long-poll is a fact, not a mode.** teled has no webhook code
path, so there is no toggle and no verb.

**slakd — stale is informational; a quiet workspace is normal.**
`/health` stays 200 with `status=stale`; death is `auth.test` failing
(`isConnected()` → 503), plus the watchdog's `auth.test`-gated
`os.Exit` backstop. This distinction is load-bearing: a stale-503 once
bounced a quiet `slakd_marinade` (2026-06-19). The dead-subscription
detection lives in the watchdog.

**reditd — 60m stale threshold** (`chanlib.staleThresholds["reddit"]`);
sparse subreddits are quiet for hours. Its `isConnected()` IS the
last-poll-vs-15m computation — the page renders that same computation,
it does not re-derive one.

**linkd — `stale` can never fire.** linkd stubs `lastInboundAt` to
`time.Now()` (`linkd/server.go`; no inbound plumbing), so the page
states that instead of showing a meaningless freshness number.

**emaid — INBOX only, hardcoded.** No multi-folder support, so
"resync folders" has no mechanism and is absent.

**whapd and twitd are TypeScript** and cannot import `chanlib`: they
re-implement the contract, including the operator dash gate
(`whapd/src/auth.ts` and `twitd/src/auth.ts` verify routd service tokens
for the adapter API; the dash gate is separate). Both also serve their
own `/health` where 5m staleness is a **503**, stricter than the chanlib
default. The theme is inlined (`6/31`).

## Acceptance

- Contract acceptance ([`6/31`](31-adapter-contract.md)) passes for all
  ten adapters.
- whapd: starting a pair shows the code + countdown; a sixth attempt in
  an hour renders the 429 in the pair pane.
- slakd: a quiet workspace renders `stale` with 200, not a 503.
- reditd: the cursor table matches the poller's own cursors, and the
  dash handler never opens `cursors.json`.
- bskyd and emaid: the delta section carries no credential
  (`TestDashSectionsCarryNoCredentials`; the emaid password test).

## Build state (2026-08-30)

All ten adapters serve `/dash/<adapter>/`: the eight Go ones from
`chanlib/dash.go` via `NewAdapterMux`, whapd and twitd from their own
`src/dash.ts` (`6/31`).

**The delta mechanism.** `chanlib.DashSectioner` is an OPTIONAL
interface — the same pattern as `HistoryProvider` — so an adapter with nothing
extra needs no code, which is the common case. A section is `[][2]string` label
/value rows rather than HTML, deliberately: handing ten adapters raw markup is
how one grammar becomes ten layouts.

**reditd's delta**, the one that mattered most. Two facts are invisible on the
shared page and an operator needs both:

- **Which sources it polls.** reditd is the single adapter that reads a feed the
  bot is not addressed in — the configured subreddits, recorded as `6/33`'s one
  boundary exception. An operator who cannot see that list cannot audit it.
- **How far each source has read.** A cursor frozen across restarts is what a
  silently-broken poll looks like, and the shared "last inbound" tile cannot
  show it: one live source keeps the whole adapter looking fresh.

A configured subreddit that has never read anything is listed as `(never read)`
rather than omitted — the absent row is exactly the one being hunted.

**bskyd's delta.** The shared page says whether the adapter is connected; it
cannot say WHICH account it is connected as, and on Bluesky that is the
operator's real question — every post is attributed to the repo DID and posting
from the wrong account is silent. The JWTs are deliberately absent: they are
bearer credentials, and a session panel that prints them turns an operator page
into a credential leak. `TestDashSectionsCarryNoCredentials` pins that.

**whapd's delta**, in TypeScript, with the same section type. It renders the
pair-flow state, which is the one thing the shared page cannot say:
"disconnected" does not tell an operator whether they must go and enter a code
on the phone. It reads `bot.getStatus()`, which already redacts the code.

**emaid's delta**, and `L62` is why it earns its place: marinade's emaid sat
IMAP-auth-failing for three days and the only visible symptom was an unhealthy
container. "Connected or not" is not actionable for email; which account
against which host is. The password is excluded and a test pins that — it is
the credential the failing login uses, and printing it would trade a diagnosis
for a leak.

**gitd's delta.** gitd is webhook-driven, so the operator's question when a
source goes quiet is which of the configured sources last delivered. The shared
"last inbound" tile answers that for the adapter as a whole and hides it
per source, exactly as reditd's cursors do. A source with nothing yet reads
`none since start` rather than being omitted (`gitd/bot.go:145-157`).

**Every adapter that has a delta has it.** The five that do — reditd, bskyd,
whapd, emaid, gitd — each render exactly the state the shared page cannot show.
The rest implement nothing, which is the interface being optional working as
intended.
