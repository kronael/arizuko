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
- Auto-resume the outbound queue from disk on reconnect.

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
- `forward` cannot true-relay because we receive `source_msg_id` only.
- `edit` and `delete` only succeed on bot-authored messages; WhatsApp
  enforces a 15-minute edit window server-side.

## Entry points

- Binary: `whapd/dist/main.js` (built via `bun build` or `tsc`)
- Image: `whapd/Dockerfile`
- Listen: `$LISTEN_ADDR` (default `:9007`)
- Router registration: `whatsapp:` prefix.

## Dependencies

- `@whiskeysockets/baileys`, `pino`, `qrcode-terminal`
- No Go imports; the `chanlib.Run` pattern is reimplemented in `server.ts`.

## Configuration

- `WHATSAPP_AUTH_DIR` (default `$DATA_DIR/store/whatsapp-auth`)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`
- `ASSISTANT_NAME`, `DATA_DIR`

## Health signal

`GET /health` returns 503 while waiting for QR scan or when the Baileys
socket is disconnected. Operators check the container log for the QR
ascii art.

## Files

- `src/main.ts` — Baileys socket, creds backup
- `src/server.ts` — HTTP adapter surface
- `src/queue.ts` — outbound queue
- `src/reply.ts` — reply metadata extraction
- `src/typing.ts` — typing refresh

## Related docs

- `specs/4/1-channel-protocol.md`
