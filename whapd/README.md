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
- Handle `/send`, `/send-file`, `/typing`.
- Back up `creds.json` atomically; recover from `.bak` on crash.
- Auto-resume the outbound queue from disk on reconnect.

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
