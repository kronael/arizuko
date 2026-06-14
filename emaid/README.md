# emaid

Email (IMAP inbound, SMTP outbound) channel adapter.

## Purpose

Polls IMAP for new messages (IDLE when supported, fallback poll), posts
inbound to the router. Sends outbound via SMTP. Tracks email threads in
a local SQLite DB so replies preserve the conversation JID and thread
References/In-Reply-To headers.

## Responsibilities

- Poll IMAP INBOX (IDLE when supported, 30s fallback poll)
- Classify inbound sender trust (Authentication-Results DMARC + optional From-domain allowlist)
- Post inbound to router with `message` (trusted) or `untrusted` Verb
- Send outbound via SMTP with correct In-Reply-To/References threading
- Track threads: `thread_id → (from_address, root_msg_id)` in local SQLite
- Serve `/send`, `/v1/history`, `/files/{uid}/{part}` endpoints
- Re-fetch attachments from IMAP on-demand (transient in-memory registry, no local storage)

## Verb support

| Verb            | Native | Notes                                                     |
| --------------- | ------ | --------------------------------------------------------- |
| `send`          | yes    | SMTP; `In-Reply-To` set from the thread's root Message-ID |
| `fetch_history` | yes    | IMAP search by Message-ID + References                    |
| `send_file`     | hint   | MIME attachments not implemented; inline in `send`        |
| `post`          | hint   | no public feed                                            |
| `like`          | hint   | no reactions                                              |
| `dislike`       | hint   | no reactions                                              |
| `delete`        | hint   | sent mail is immutable                                    |
| `edit`          | hint   | sent mail is immutable                                    |
| `forward`       | hint   | no native primitive; `Fwd:` is a re-styled `send`         |
| `quote`         | hint   | no quote primitive; inline the quoted text                |
| `repost`        | hint   | not a feed                                                |

## Entry points

- Binary: `emaid/main.go`
- Listen: `$LISTEN_ADDR` (default `:8080`)
- Router registration: `email:` prefix, caps `send_text`, `fetch_history`

## Dependencies

- `chanlib`

## Configuration

- `EMAIL_IMAP_HOST`, `EMAIL_IMAP_PORT` (default `993`)
- `EMAIL_SMTP_HOST`, `EMAIL_SMTP_PORT` (default `587`)
- `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`
- `EMAIL_TRUSTED_AUTHSERV` — upstream MTA whose Authentication-Results headers we trust (e.g. `mx.google.com`); empty = fail-closed
- `EMAIL_TRUSTED_DOMAINS` — comma-separated From-domain allowlist (optional; DMARC pass alone is sufficient when unset)
- `EMAIL_STRICT_AUTH` — `true` drops untrusted inbound; default `false` (deliver with `untrusted` Verb)
- `EMAIL_UNVERIFIED_SUBJECT_PREFIX` — `true` prefixes `[UNVERIFIED]` on untrusted subjects; default `false` (signal in Verb only)
- `EMAIL_VERIFY_DKIM` — tier-3 independent DKIM verification (pre-wired, not yet implemented)
- `CHANNEL_NAME` (default `email`)
- `ROUTER_URL`, `CHANNEL_SECRET`, `LISTEN_ADDR` (default `:8080`), `LISTEN_URL` (default `http://email:9003`), `DATA_DIR` (default `/srv/data/emaid`), `MEDIA_MAX_FILE_BYTES` (default 20 MiB)

## Health signal

`GET /health` returns 503 when IMAP login failed or poll loop is erroring.

## Files

- `main.go` — wiring, config
- `imap.go` — IMAP poll loop, IDLE support, attachment registry
- `smtp.go` — SMTP outbound
- `store.go` — local thread tracking DB
- `server.go` — adapter handlers (`/send`, `/v1/history`, `/files/{uid}/{part}`)
- `auth.go` — inbound sender trust classifier (Authentication-Results + From-domain allowlist)

## Related docs

- `specs/4/1-channel-protocol.md`
