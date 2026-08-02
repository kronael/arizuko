---
status: draft
depends: [1-cockpit-index, 11-adapter-contract]
---

# Adapter dashboards — the ten channel adapters

Instantiates the adapter contract ([`7/11`](11-adapter-contract.md))
for all ten adapters. The contract owns the page grammar, show matrix,
control verbs, health semantics, auth, and HTMX fragment set — none of
it is restated. This spec is the per-adapter delta table plus the
handful of facts that do not generalise.

Nothing here is built.

## Transport, liveness, identity, state

| Adapter | Transport                                                | `isConnected()` source                         | Identity                     | Persisted state                                   |
| ------- | -------------------------------------------------------- | ---------------------------------------------- | ---------------------------- | ------------------------------------------------- |
| whapd   | Baileys websocket (WhatsApp), TypeScript                 | `connection.update` flag (`whapd/src/main.ts`) | phone JID from Baileys creds | `$WHATSAPP_AUTH_DIR/creds.json` (+ `.bak`)        |
| teled   | Bot API long-poll, 30s `getUpdates`                      | poll success flag (`teled/bot.go`)             | `api.Self.UserName` + ID     | offset file `$DATA_DIR/teled-offset-<channel>`    |
| slakd   | Events API webhook push (`/slack/events`)                | 60s `auth.test` probe (`slakd/bot.go`)         | `BotUserID()` + `TeamID()`   | none                                              |
| mastd   | user-stream websocket                                    | `streaming` flag (`mastd/client.go`)           | `me.Acct`                    | none                                              |
| bskyd   | notifications poll, 10s                                  | `authed` flag (`bskyd/client.go`)              | `session.DID`                | `bluesky-session.json`                            |
| reditd  | inbox + subreddit poll, `REDDIT_POLL_INTERVAL` (5m)      | last successful poll < 15m (`pollStaleAfter`)  | `cfg.Username`               | `cursors.json`                                    |
| linkd   | own-shares comment poll, `LINKEDIN_POLL_INTERVAL` (300s) | `authed` flag (`linkd/client.go`)              | `meURN` + `meName`           | `linkd-state-<name>.json`                         |
| discd   | Discord gateway websocket (discordgo)                    | `session.DataReady` (`discd/bot.go`)           | `session.State.User`         | none                                              |
| emaid   | IMAP IDLE (28m keepalive) + 30s poll fallback; SMTP dial | `connected` flag (`emaid/imap.go`)             | `EMAIL_ACCOUNT`              | `emaid.db` (thread tables)                        |
| twitd   | mentions poll, `TWITTER_POLL_INTERVAL` (90s), TypeScript | `connected` flag (`twitd/src/main.ts`)         | cookie session (handle)      | `$TWITTER_AUTH_DIR/cookies.json` + `cursors.json` |

## Controls and extra sections

An affordance with no underlying mechanism is **absent from the page**,
not greyed out (`7/11`). Everything marked "new" is Required work.

| Adapter | Controls                                                                      | Extra sections beyond the contract                                                   |
| ------- | ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| whapd   | start pairing + pair status (**exist**); session reset (new)                  | pairing pane (state machine, code + 60s expiry, 5/hour bucket); outbound queue depth |
| teled   | reconnect (new — recreate `BotAPI`, restart poll loop)                        | poll offset + last poll error with its 3s backoff                                    |
| slakd   | refresh auth (new — run `authTest`, flip `connected`, reset watchdog counter) | watchdog pane: `SLAKD_STALE_SECONDS`, fail counter vs `SLAKD_STALE_FAIL_LIMIT`       |
| mastd   | reconnect (new — cancel stream ctx → immediate redial, backoff reset)         | backoff policy fact line (exponential 1s→60s, resets on success)                     |
| bskyd   | refresh auth (new — `refreshSession` → `createSession` fallback)              | JWT session state, persisted-session file mtime                                      |
| reditd  | reconnect (new — immediate `pollOnce`); refresh auth (new — `refreshToken`)   | cursor table (`inbox` + one `sr:<subreddit>` row each); rate-limit pane              |
| linkd   | reconnect (new — immediate `pollOnce`); refresh auth (new)                    | OAuth token expiry; the stub-inbound caveat below                                    |
| discd   | reconnect (new — `session.Close()` + `Open()`)                                | guild/channel cache **counts** (read-only); send-retry policy pane                   |
| emaid   | reconnect (new — close IMAP client, retry loop redials)                       | active inbound mode (`idle` vs `poll` fallback); thread-store row counts             |
| twitd   | refresh auth, session reset, reconnect (all new)                              | cookie state + login validity; per-source cursors                                    |

Each delta field rides the adapter's `GET /v1/status` (the contract's
one read endpoint) — a dash handler never opens `cursors.json`,
`cookies.json`, or `emaid.db` directly (`7/1` read-path).

Adapters with **no** control at all in a column are stating a fact, not
deferring: mastd and discd hold static tokens, so there is no auth
refresh; slakd's Events API is push, so there is no connection to
re-establish; emaid's IMAP/SMTP password is static; bskyd's session
file only feeds `createSession`, which refresh-auth already covers.

## Adapter-specific facts

**whapd — Baileys reconnection is internal.** No `reconnect` verb: the
only meaningful recoveries are re-pair and reset. The dashboard is the
operator UI for the **pairing-code** path, preferred over reading a QR
out of container logs (see `whapd/README.md`, the `--pair <phone>`
flag); the pair rate limit's 429 surfaces verbatim. QR rendering stays
out of the dashboard.

**teled — long-poll is a fact, not a mode.** teled has no webhook code
path, so there is no toggle and no verb.

**slakd — stale is informational; a quiet workspace is normal.**
`/health` stays 200 with `status=stale`; death is `auth.test` failing
(`isConnected()` → 503), plus the watchdog's `auth.test`-gated
`os.Exit` backstop. This distinction is load-bearing: a stale-503 once
bounced a quiet `slakd_marinade` (2026-06-19). The dead-subscription
detection that 503 was added for (2026-06-05) now lives in the
watchdog.

**reditd — 60m stale threshold** (`chanlib.staleThresholds["reddit"]`);
sparse subreddits are quiet for hours. Its `isConnected()` IS the
last-poll-vs-15m computation — the page renders that same computation,
it does not re-derive one.

**linkd — `stale` can never fire.** linkd stubs `lastInboundAt` to
`time.Now()` (`linkd/server.go`; no inbound plumbing), so the page
states that instead of showing a meaningless freshness number.

**emaid — INBOX only, hardcoded.** No multi-folder support, so
"resync folders" has no mechanism and is absent. SMTP is a per-message
dial, so the contract's shared `lastOutboundAt` doubles as the SMTP
health signal.

**whapd and twitd are TypeScript** and cannot import `chanlib`: they
re-implement the contract, including the operator dash gate, which is
**new work** — today `whapd/src/auth.ts` and `twitd/src/auth.ts` verify
only routd service tokens. Both also serve their own `/health` where
5m staleness is a **503**, stricter than the chanlib default. Theme is
vendored from `theme/` into the TS images at build time — one source,
copied by the image build, never hand-edited downstream.

## Non-goals beyond `7/11`

- No cursor editing or rewind (reditd, twitd) — cursors advance only on
  successful delivery by design; rewind means replay.
- No follow / subreddit / guild management, no Slack pane-staging
  controls, no Discord admin, no mailbox browsing, no posting UI.
  Content lives in routd; `/v1/pane/*` is an agent-output mechanism.
- No QR rendering (whapd, above).

## Acceptance

- Contract acceptance ([`7/11`](11-adapter-contract.md)) passes for all
  ten adapters.
- whapd: starting a pair shows the code + countdown; a sixth attempt in
  an hour renders the 429 in the pair pane; session-reset flips status
  to `disconnected` and pair state to `unauthenticated`.
- teled: reconnect restarts polling without losing the offset — no
  message replay, no gap.
- slakd: auth-refresh resets the fail counter; a quiet workspace renders
  `stale` with 200, not a 503.
- mastd / discd: reconnect redials within one backoff cycle and status
  returns to `ok` on the next inbound; discd's cache counts repopulate.
- bskyd / linkd / reditd: auth-refresh updates the rendered token
  expiry; a failed refresh flips status to `disconnected`.
- reditd: the cursor table matches what `/v1/status` reports, and the
  dash handler never opens `cursors.json`.
- emaid: the page shows the active inbound mode; reconnect from
  poll-fallback re-attempts IDLE; a send updates last-outbound.
- twitd: auth-refresh after expired cookies restores `ok`;
  session-reset deletes the cookies and leaves cursors untouched.
