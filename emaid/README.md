# emaid

Email (IMAP inbound, SMTP outbound) channel adapter.

## Purpose

Polls IMAP for new messages (IDLE when supported, fallback poll), posts
inbound to the router. Sends outbound via SMTP. Tracks email threads in its
own store so replies preserve the conversation JID and thread
References/In-Reply-To headers.

## Responsibilities

- Poll IMAP INBOX (IDLE when supported, 30s fallback poll)
- Classify inbound sender trust (Authentication-Results DMARC + optional From-domain allowlist)
- Post inbound to router with `message` (trusted) or `untrusted` Verb
- Send outbound via SMTP with correct In-Reply-To/References threading
- Track threads: `thread_id → (from_address, root_msg_id)`, and every seen
  `Message-ID → thread_id`, in `emaid/emaistore`
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
- Listen: `$LISTEN_ADDR` (code default `:9003`; the compose fragment pins `:8080`)
- Router registration: `email:` prefix, caps `send_text`, `fetch_history`
- `GET /dash/emaid/` — the operator page, mounted for free by
  `chanlib.NewAdapterMux` from the one renderer in `chanlib/dash.go` (spec
  6/31). Routed by DAEMON name (`AUTHD_SERVICE_NAME`), never by platform.

## Dependencies

- `chanlib`

## Configuration

- `EMAIL_IMAP_HOST`, `EMAIL_IMAP_PORT` (default `993`)
- `EMAIL_SMTP_HOST`, `EMAIL_SMTP_PORT` (default `587`)
- `EMAIL_ACCOUNT`
- `EMAIL_PASSWORD` or `EMAIL_ACCESS_TOKEN` — set exactly one. Both set, or neither,
  stops the boot. The password uses IMAP `LOGIN` and SMTP `PLAIN`; the token uses
  SASL `XOAUTH2` on both (format: `xoauth2.go`), which is what Gmail and other
  OAuth-only mailboxes require.
- `EMAIL_TRUSTED_AUTHSERV` — upstream MTA whose Authentication-Results headers we trust (e.g. `mx.google.com`); empty = fail-closed
- `EMAIL_TRUSTED_DOMAINS` — comma-separated From-domain allowlist (optional; DMARC pass alone is sufficient when unset)
- `EMAIL_STRICT_AUTH` — `true` drops untrusted inbound; default `false` (deliver with `untrusted` Verb)
- `EMAIL_UNVERIFIED_SUBJECT_PREFIX` — `true` prefixes `[UNVERIFIED]` on untrusted subjects; default `false` (signal in Verb only)
- `EMAIL_VERIFY_DKIM` — tier-3 independent DKIM verification (pre-wired, not yet implemented)
- `CHANNEL_NAME` (default `email`)
- `ROUTER_URL`, `AUTHD_URL`, `AUTHD_SERVICE_KEY`, `AUTHD_SERVICE_NAME`, `LISTEN_ADDR` (default `:9003`), `LISTEN_URL` (default `http://email:9003`), `MEDIA_MAX_FILE_BYTES` (default 20 MiB)

## Access token expiry

`EMAIL_ACCESS_TOKEN` is read once and never refreshed. An access token lasts about an
hour (Google). When it expires the next IMAP login, on a reconnect, is refused. The
poller stops, `/health` returns 503 and the log names the rejected credential only when
the refusal carries the response code `AUTHENTICATIONFAILED` or `AUTHORIZATIONFAILED`
(`emaid/imap.go:119-135`). Google documents a bare `NO SASL authentication failed`,
which the backoff retries (BUGS `L425`). An SMTP send with an expired token returns
`sendReply`'s plain error (`emaid/server.go:183`), so routd retries the reply ten times
and tells the agent it is queued (BUGS `L425`). To recover, set a new token and restart
the instance. A refresh path (for example a Google service account) is not built.

## Health signal

`GET /health` is chanlib's. It returns 503 `disconnected` while the IMAP
session is down: the `connected` flag drops when login fails or the poll loop
errors.

The adapter dashboard adds a **Mailbox** section: the account, the IMAP host
and the SMTP host. The password is never rendered.

## Files

- `main.go` — wiring, config, the one-credential check
- `xoauth2.go` — XOAUTH2 initial response for IMAP and SMTP
- `imap.go` — IMAP poll loop, IDLE support, attachment registry
- `smtp.go` — SMTP outbound
- `storage.go` — opens emaid's own tables on the instance's store server
- `emaistore/` — the storage layer: two entities, no SQL
- `server.go` — adapter handlers (`/send`, `/v1/history`, `/files/{uid}/{part}`)
- `auth.go` — inbound sender trust classifier (Authentication-Results + From-domain allowlist)

## Storage

emaid holds NO SQL and no `CREATE TABLE`. Its storage is
`emaid/emaistore`, which is `objstore`'s two primitives over two entities:

| entity          | coordinate                     | attributes                    |
| --------------- | ------------------------------ | ----------------------------- |
| `email_threads` | the thread id, as both halves  | `from_address`, `root_msg_id` |
| `email_msg_ids` | the Message-ID, as both halves | `thread_id`                   |

Every read is a point read, so the partition is the identifier rather than a
constant naming the daemon: nothing wants two items co-located, and
`email_msg_ids` gains an item per inbound mail with no sweep.

chanlib's `outbound_dedup` and `inbound_pending` sit on the same store as
emaid's own entities and are chanlib's to declare, read through
`chanlib/chanstore`. emaid opens through `chanlib.OpenAdapterStore`, the same
door the other seven adapters use, and adds its own entities to the driver that
door hands back.

## Related docs

- [`chanlib/README.md`](../chanlib/README.md) — the channel protocol every adapter speaks

## Coverage

Config, the storage bootstrap, the MIME/attachment extraction, the thread
store, the outbound path and every unimplemented-verb refusal are covered.
`emaistore`'s own bodies run against a DynamoDB endpoint, which is the store
production has.

`go test ./emaid/ -short -cover` reports 66.4% (2026-09-13). The number
stops there because the IMAP poller — `newPoller`, `run`,
`runPoll`, `FetchHistory` — needs a real IMAP server, and `defaultSMTPSender`
needs a real submission port. Mocking either would assert the mock. Those are
exercised against a live mailbox by `make smoke`.
