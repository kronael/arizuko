---
status: draft
depends: [1-cockpit-index, 11-adapter-contract]
---

# whapd + teled + slakd dashboards — session chat adapters

**Purpose** — instantiate the adapter contract
([`6/11`](11-adapter-contract.md)) for the three session chat
adapters. Everything not listed here is the contract verbatim.

| Adapter | Transport                                 | `isConnected()` source                       | Identity                     | Persisted state                                |
| ------- | ----------------------------------------- | -------------------------------------------- | ---------------------------- | ---------------------------------------------- |
| whapd   | Baileys websocket (WhatsApp)              | `connection.update` flag (`src/main.ts`)     | phone JID from Baileys creds | `$WHATSAPP_AUTH_DIR/creds.json` (+ `.bak`)     |
| teled   | Bot API long-poll, 30s `getUpdates`       | poll success flag (`teled/bot.go connected`) | `api.Self.UserName` + ID     | offset file `$DATA_DIR/teled-offset-<channel>` |
| slakd   | Events API webhook push (`/slack/events`) | 60s `auth.test` probe (`slakd/bot.go`)       | `BotUserID()` + `TeamID()`   | none (pane staging via routd `/v1/pane`)       |

## whapd deltas

Show:

- Pairing pane (extra section + `x/pair` fragment): pair state machine
  `idle | requesting | pending | unauthenticated`, active code +
  `expires_at` (60s TTL), pair rate-limit bucket (5 attempts/hour →
  the contract's rate-limit section).
- Outbound queue depth — whapd queues sends while disconnected and
  flushes on reconnect; expose the in-memory queue length via
  `/v1/status`.
- Health note: whapd serves its own `/health` (TS); 5m staleness is a
  **503**, stricter than the chanlib default informational stale.

Control:

- **Start pairing** — existing `POST /v1/pair/start` (returns code +
  expiry) and `GET /v1/pair/status`; the dashboard is the operator UI
  for the pairing-code path (preferred over scanning the QR from
  container logs — see `whapd/README.md` and the `--pair <phone>` CLI
  flag). Surfacing 429 from the pair rate limit verbatim.
- **Reset session** — contract `session-reset`: delete
  `$WHATSAPP_AUTH_DIR` creds, account drops offline until re-paired.
  `.btn-danger`, confirm. New verb.
- No `reconnect` / `auth/refresh` — Baileys reconnection is internal;
  the only meaningful recovery actions are re-pair and reset.

TS: whapd implements the gate + vendored theme per `6/11` "TS
adapters".

## teled deltas

Show:

- Mode is static **long-poll** (teled has no webhook code path); shown
  as a transport fact, no mode toggle — no verb exists, so no control
  (`6/11`: absent, not greyed out).
- Poll offset (the persisted `getUpdates` offset) + last poll error
  with its 3s-backoff retry note.

Control:

- **Reconnect** — contract `POST /v1/reconnect`: recreate the
  `BotAPI` client and restart the poll loop. New verb.
- Nothing else: token-only auth (`TELEGRAM_BOT_TOKEN`), no session to
  reset, no refresh path in code.

## slakd deltas

Show:

- Watchdog pane: `SLAKD_STALE_SECONDS` (default 300) silence
  threshold, consecutive `auth.test` fail counter vs
  `SLAKD_STALE_FAIL_LIMIT` (default 5 → process exit), last probe
  result + time (`slakd/bot.go` watchdog + health probe).
- Strict-stale note: slakd is in `chanlib.strictStale` — stale returns
  503 so Docker restarts it (the 11h silent Events-API outage,
  2026-06-05).
- Webhook signature verification on (HMAC of `v0:<ts>:<body>`) —
  static fact line.

Control:

- **Refresh auth** — contract `POST /v1/auth/refresh`: run `authTest`
  now, flip `connected`, reset the watchdog fail counter on success.
  New verb wrapping the existing probe.
- No `reconnect`: Events API is push; slakd holds no connection to
  re-establish. No session reset: token lives in env.

## Required `/v1` work

Beyond the shared chanlib work in `6/11`:

| Verb / field                             | Adapter | Wraps                                  |
| ---------------------------------------- | ------- | -------------------------------------- |
| `POST /v1/session/reset`                 | whapd   | delete Baileys auth dir + end socket   |
| queue depth + pair state in `/v1/status` | whapd   | in-memory queue + pair state machine   |
| `POST /v1/reconnect`                     | teled   | recreate `BotAPI`, restart poll loop   |
| offset in `/v1/status`                   | teled   | persisted offset file value            |
| `POST /v1/auth/refresh`                  | slakd   | `authTest()` + watchdog counter reset  |
| watchdog state in `/v1/status`           | slakd   | fail counter, limit, last probe result |

## Auth / HTMX / non-goals

Per [`6/11`](11-adapter-contract.md) and [`6/1`](1-cockpit-index.md).
Extra fragment: whapd `GET /dash/whapd/x/pair` (poll `every 2s` while
state is `requesting|pending`), `POST x/pair-start`.

Non-goal additions: no QR rendering in the dashboard (pairing-code
path only; QR stays a container-log fallback); no Slack pane-staging
controls (`/v1/pane/*` is an agent-output mechanism, not an operator
control).

## Acceptance

- Contract acceptance (`6/11`) passes for all three.
- whapd: starting a pair from the page shows the code + countdown;
  exceeding 5 attempts/hour renders the 429 in the pair pane;
  session-reset flips status to `disconnected` and pair state to
  `unauthenticated`.
- teled: reconnect restarts polling without losing the offset (no
  message replay, no gap).
- slakd: auth-refresh resets the fail counter; a stale slakd page
  shows status `stale` with 503 semantics noted.
