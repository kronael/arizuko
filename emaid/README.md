# emaid

Email (IMAP inbound, SMTP outbound) channel adapter.

## Purpose

Polls IMAP for new messages, posts inbound to the router. Sends outbound
via SMTP. Tracks email threads in a local sqlite (`store.go`) so replies
preserve the conversation JID.

## Responsibilities

- Connect to IMAP (`EMAIL_IMAP_HOST:PORT`), poll INBOX.
- Send via SMTP (`EMAIL_SMTP_HOST:PORT`).
- Keep a thread table: `thread_id → chat_jid` (matches `email_threads` in messages.db).
- Handle `/send`, `/v1/history`.
- Serve attachments through an in-process registry.

## Entry points

- Binary: `emaid/main.go`
- Listen: `$LISTEN_ADDR` (default `:9003`)
- Router registration: `email:` prefix, caps `send_text`, `fetch_history`.

## Dependencies

- `chanlib`

## Configuration

- `EMAIL_IMAP_HOST`, `EMAIL_IMAP_PORT` (default `993`)
- `EMAIL_SMTP_HOST`, `EMAIL_SMTP_PORT` (default `587`)
- `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`
- `EMAIL_STRICT_AUTH` (`true` rejects unsigned senders)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR`, `LISTEN_URL`, `DATA_DIR`, `MEDIA_MAX_FILE_BYTES`

## Health signal

`GET /health` returns 503 when IMAP login failed or poll loop is erroring.

## Files

- `main.go` — wiring
- `imap.go` — poll loop
- `smtp.go` — outbound
- `store.go` — local thread tracking
- `server.go` — adapter handlers

## Related docs

- `specs/4/1-channel-protocol.md`
