# whapd

WhatsApp channel adapter (TypeScript, Baileys).

## Purpose

Bridges WhatsApp Web (Baileys socket) to the router. Only TypeScript
daemon in the repo — Baileys has no Go port. Persists pairing credentials
to `$WHATSAPP_AUTH_DIR`; a fresh install prints a QR code to the log
until scanned.

## Responsibilities

- Pair via QR, maintain Baileys websocket.
- Post inbound as `whatsapp:<jid>` to router; download media.
- Handle social-action HTTP surface (see Verb support).
- Back up `creds.json` atomically; recover from `.bak` on crash.
- Hold no outbound queue of its own: `/send` returns 502 when the socket is
  down or the send throws, and routd's durable retry sweep owns the retry.

## Verb support

| Verb        | Status      | Notes                                                                 |
| ----------- | ----------- | --------------------------------------------------------------------- |
| `send`      | native      | `sock.sendMessage(jid, {text})`; honours `reply_to` via `quoted` stub |
| `send_file` | native      | image/video/audio/document by mime sniff                              |
| `typing`    | native      | composing/paused presence with refresher                              |
| `like`      | native      | reaction; `reaction` field selects emoji (default 👍)                 |
| `edit`      | native      | own messages within the WhatsApp 15-min window                        |
| `delete`    | native      | own-message redaction via `{delete: <key>}`                           |
| `forward`   | best-effort | synthesizes a forwarded text (no source jid available to relay)       |
| `quote`     | 501 hint    | use `send` with `reply_to` (Baileys `quoted`)                         |
| `repost`    | 501 hint    | not a feed; suggest `forward`                                         |
| `dislike`   | 501 hint    | use `like(reaction='👎')`                                             |
| `post`      | 501 hint    | no public-feed primitive; use `send`                                  |

## Limitations

- No `fetch_history`. Baileys' history sync is unreliable across LID/JID
  translation; the gateway falls back to its local-DB cache.
- `forward` cannot true-relay because Baileys delivers `source_msg_id` only.
- `edit` and `delete` only succeed on bot-authored messages; WhatsApp
  enforces a 15-minute edit window server-side.

## Entry points

- Binary: `whapd/dist/main.js` (built via `bun build` or `tsc`)
- Image: `whapd/Dockerfile`
- Listen: `$LISTEN_ADDR` (code default `:9002`; deployment TOML sets `:8080`)
- Router registration: `whatsapp:` prefix.

## Dependencies

- `@whiskeysockets/baileys`, `pino`, `qrcode-terminal`
- No Go imports; the `chanlib.Run` pattern is reimplemented in `server.ts`.

## Configuration

- `WHATSAPP_AUTH_DIR` (default `$DATA_DIR/store/whatsapp-auth`)
- `WHAPD_STATE_DIR` (default `$DATA_DIR/store/whapd`) — the outbound dedup
  ledger. MUST be mounted: an unmounted one forgets every claim on recreate,
  which is the same as having none.
- `ROUTER_URL`, `AUTHD_URL`, `AUTHD_SERVICE_KEY`, `AUTHD_SERVICE_NAME`, `LISTEN_ADDR`, `LISTEN_URL`
- `ASSISTANT_NAME`, `DATA_DIR`

## Outbound dedup

routd re-sends a bot row that stays `pending`, so the same
`idempotency_key` arrives at `/send` and `/send-file` more than once by
design. whapd claims the key in a durable ledger BEFORE calling WhatsApp and
settles it after, so a resend of a delivered key returns the recorded id and
posts nothing.

The ledger is a DIRECTORY OF FILES, one per key, not a database: node:22 has
no stable `node:sqlite`, and the only operation that must be atomic is the
claim — which `open(path, 'wx')` (`O_CREAT|O_EXCL`) already is.

Failure handling is ordering, not error parsing. Baileys exposes no signal
separating "WhatsApp refused it" from "the ack was lost", so whapd never
guesses: it HOLDS the claim on any error after the call and answers `409` on
the next attempt, which routd dead-letters for an operator instead of
double-posting. The transient that would otherwise become a guaranteed
dead-letter is caught earlier — a socket that is not open refuses the send
BEFORE any claim exists, so a routd restart answers 502 with nothing
recorded and the sweep retries freely.

## Health signal

`GET /health` returns 503 while waiting for QR scan or when the Baileys
socket is disconnected. Operators check the container log for the QR
ascii art. whapd serves its own `/health`, and a channel quiet past 5 min
answers 503 `stale`. That is stricter than chanlib's Go adapters, where
`stale` is 200. routd's health loop reads the status word and still counts a
`stale` whapd as alive; anything that reads only the HTTP code sees a
failure.

## Staying registered

whapd never exits on a failed registration: it retries. A restart loop would
race Baileys' non-atomic writes of `creds.json` and corrupt the session.
whapd copies `creds.json` to `creds.json.bak` and restores the copy when a
crash left the live file empty. After 10 failed reconnects in a row it exits,
and the container restart takes over.

## Re-pairing without the log

`POST /v1/pair/start {phone}` links the account to a new device by a pairing
code instead of a QR scan. `GET /v1/pair/status` reports the state: `idle`,
`requesting`, `pending` or `unauthenticated`. Both sit behind the same
`service:routd` bearer check as `/send` (`verifyRoutd`, `src/auth.ts`).

- **The code is returned once**, by `start`. `status` never carries it,
  because anyone who can read `status` could then link a device.
- **One flow at a time.** A mutex guards the flow; a second `start` while one
  is `requesting` or `pending` gets 429. The code is valid for 60 s, and the
  reconnect loop is suspended while a flow runs.
- **At most 5 starts per rolling hour** per process, then 429 "too many
  attempts". WhatsApp bans a number that makes too many link attempts.

The dashboard shows the pair-flow state as its own section (`src/dash.ts`).

## Files

- `src/main.ts` — Baileys socket, creds backup, listen wiring
- `src/server.ts` — HTTP adapter surface
- `src/inbound.ts` — pure inbound-dispatch helpers (unit-testable, sink in main.ts)
- `src/bot.ts` — pair-flow state machine
- `src/client.ts` — router client (register/inject)
- `src/dedup.ts` — outbound dedup ledger (file-per-key claim/settle/release)
- `src/reply.ts` — reply metadata extraction
- `src/typing.ts` — typing refresh
- `src/log.ts` — slog-style structured logging
- `src/auth.ts` — the ES256 service-token boundary, both halves. Mirrors the Go
  `chanlib.Auth` path: exchange `AUTHD_SERVICE_KEY` at authd for a short-TTL
  `service:whapd` JWT outbound, verify routd's token offline inbound
- `src/dash.ts` — `/dash/whapd/`. The Go adapters get this page from
  `chanlib/dash.go` for free; whapd cannot import chanlib, so this is a second
  implementation of the same page, not a second design. Its capability list
  is declared (`supportedVerbs`), not probed, so keep it in step

## Related docs

- [`chanlib/README.md`](../chanlib/README.md) — the channel protocol and the delivery contract whapd keeps by hand
