# emaid

emaid receives mail through IMAP and sends replies through SMTP. It owns the `emaid.*` adapter entities and email threading indexes.
It marks inbound mail seen only after routing succeeds or the configured trust policy drops it.

[Architecture](../ARCHITECTURE.md) covers the system; [channel protocol](../chanlib/README.md) defines the shared wire format.

```text
IMAP INBOX ──► emaid ──► routd:8080
routd ──► emaid:8080 ──► SMTP
emaid ──► authd:8080 and the store
```

## HTTP interfaces

| Method and path                                                                                          | Caller and behavior                                                           |
| -------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `POST /send`, `/send-file`                                                                               | routd; text or multipart file delivery with an optional `idempotency_key`.    |
| `POST /send-voice`, `/typing`                                                                            | routd; audio upload or activity indicator.                                    |
| `POST /post`, `/like`, `/delete`, `/forward`, `/quote`, `/repost`, `/dislike`, `/edit`, `/pin`, `/unpin` | routd; platform actions. Unsupported actions return 501.                      |
| `GET /health`                                                                                            | Public platform status and pending inbound count.                             |
| `GET /dash/<daemon>/`                                                                                    | Operator page; compose supplies the daemon name through `AUTHD_SERVICE_NAME`. |
| `GET /files/<id>`                                                                                        | routd bearer; serves an attachment from the process attachment registry.      |
| `GET /v1/history?jid=<jid>&before=<RFC3339>&limit=<n>`                                                   | routd bearer; fetches mail history.                                           |

The standard adapter routes use `chanlib.Auth`: only `service:routd` passes when `AUTHD_URL` is set.
The dashboard accepts the proxyd-attested operator identity.
The Go entry point requires `AUTHD_URL` and `AUTHD_SERVICE_KEY` even though the shared handler supports an open development gate.
This daemon mounts no resreg resources, MCP socket, `/openapi.json`, or `/metrics`.
Agent messaging tools run in routd and call these HTTP routes.

Startup registration, store setup, or listener failure exits the process.
The router client registers again every 3 minutes and tries inbound delivery twice, with a 500 ms wait between attempts.
Each router HTTP call has a 60 s timeout. Graceful HTTP shutdown allows 10 s.
`/health` returns 503 for a disconnected platform and 200 for connected or `stale` status.
A failed pending-count read logs `health: pending inbound count failed`; it does not change the HTTP status.
A healthy adapter does not prove routd accepted messages.

## State and delivery

`StoreReach["emaid"]` opens only `emaid`.
`email_threads` stores the reply address and root Message-ID for each conversation.
`email_msg_ids` maps each received Message-ID to its thread. No pruning loop clears this index.
The IMAP server holds unread mail across outages. The attachment registry is process-local.
The adapter uses temporary files for attachments, not a durable local mail spool.

| Entity            | Contents                                                                 |
| ----------------- | ------------------------------------------------------------------------ |
| `outbound_dedup`  | Delivery key, inflight claim, settled platform ID.                       |
| `inbound_pending` | Retained inbound payload and retry schedule, where retention is enabled. |
| `file_urls`       | Opaque attachment IDs and platform download URLs, where used.            |
| `ack_marks`       | Per-chat acknowledgement reaction and expiry, where used.                |

The adapter creates these entities through `chanlib.OpenAdapterStore`.
`/send` and `/send-file` deduplicate keyed deliveries. Other verbs do not use that ledger.
An uncertain platform outcome keeps the claim; another attempt returns 409 for operator review.
A proven non-delivery can release the claim. Store failure refuses a keyed send before a platform call.

## Configuration

Defaults below come from the Go code. Container fragments set `LISTEN_ADDR=:8080` and advertise the compose service on port 8080.

| Variable                                     | Code default        | Meaning                                                                                          |
| -------------------------------------------- | ------------------- | ------------------------------------------------------------------------------------------------ |
| `EMAIL_IMAP_HOST`, `EMAIL_SMTP_HOST`         | required            | Incoming and outgoing server hosts.                                                              |
| `EMAIL_ACCOUNT`                              | required            | Account address and login name.                                                                  |
| `EMAIL_PASSWORD`                             | unset               | Password or app password; required without an access token.                                      |
| `EMAIL_ACCESS_TOKEN`                         | unset               | XOAUTH2 credential; takes priority over the password. emaid does not refresh it.                 |
| `EMAIL_IMAP_PORT`                            | `993`               | IMAP TLS port.                                                                                   |
| `EMAIL_SMTP_PORT`                            | `587`               | SMTP submission port; SMTP requires STARTTLS.                                                    |
| `EMAIL_TRUSTED_AUTHSERV`                     | unset               | Authentication-Results server ID to trust; unset classifies all mail as untrusted.               |
| `EMAIL_TRUSTED_DOMAINS`                      | unset               | Comma-separated From-domain allowlist, checked after DMARC pass.                                 |
| `EMAIL_STRICT_AUTH`                          | false               | Drop untrusted mail.                                                                             |
| `EMAIL_UNVERIFIED_SUBJECT_PREFIX`            | false               | Prefix untrusted subjects with `[UNVERIFIED] `.                                                  |
| `EMAIL_VERIFY_DKIM`                          | false               | Requests unimplemented independent DKIM verification; startup warns.                             |
| `ROUTER_URL`                                 | required            | routd base URL.                                                                                  |
| `CHANNEL_NAME`                               | `email`             | Registration name; prefix stays `email:`.                                                        |
| `LISTEN_ADDR`                                | `:9003`             | Code listener; compose uses `:8080`.                                                             |
| `LISTEN_URL`                                 | `http://email:9003` | Advertised address; compose uses `http://emaid:8080`.                                            |
| `MEDIA_MAX_FILE_BYTES`                       | `20971520`          | Attachment size cap in bytes.                                                                    |
| `AUTHD_URL`                                  | required            | authd URL, normally `http://authd:8080`; JWKS and service-token exchange.                        |
| `AUTHD_SERVICE_KEY`                          | required            | Bootstrap secret exchanged for a service JWT.                                                    |
| `AUTHD_SERVICE_NAME`                         | channel name        | Exchange as the base daemon name; compose sets this explicitly. Also selects the dashboard path. |
| `DYNAMODB_URL`                               | required            | Store endpoint; tables use the daemon's fixed prefix.                                            |
| `AWS_REGION`                                 | `us-east-1`         | Store signing region.                                                                            |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | required            | This daemon's store identity.                                                                    |
| `STORE_CA`                                   | unset               | Base64 PEM certificate for the store endpoint; unset uses system trust.                          |
| `ARIZUKO_INSTANCE`                           | unset               | Observability instance label.                                                                    |
| `LOG_LEVEL`                                  | `info`              | JSON stderr log level: debug, info, warn, error.                                                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                | unset               | Optional OTLP log export; see [obs](../obs/README.md).                                           |

## Loops and failure diagnosis

IMAP IDLE reconnects after errors with 1 s exponential backoff capped at 60 s.
IDLE renews every 28 minutes and fetches unseen mail after renewal or mailbox notification.
If IDLE is unavailable, polling runs every 30 s with the same 1–60 s error backoff.
An authentication rejection in the IDLE path stops the receiver goroutine and leaves HTTP up with disconnected health.
The fallback poll path keeps retrying errors; credentials require operator replacement and restart.
The IDLE shutdown wait forces the connection closed after 5 s.
Connected silence over 10 minutes reports `stale` for the default `email` name; other names use 5 minutes.
SMTP failure returns an adapter error to routd. Inbound router failure leaves mail unseen for another fetch.
Grep `imap idle error`, `imap poll error`, `imap: the server rejected`, and `EMAIL_TRUSTED_AUTHSERV unset`.

## Limits and verification

Only `send_text` and `fetch_history` are advertised. File, voice, social actions, and typing have no email implementation.
Trusted mail needs DMARC pass from the pinned authserv and, when configured, an allowed From domain.
emaid parses upstream Authentication-Results; it does not perform cryptographic DKIM verification.
[IMAP tests](imap_test.go) exercise connection behavior with stubs; live mailbox and SMTP delivery remain integration checks.

Sources: [configuration](main.go), [IMAP loop](imap.go), [SMTP](smtp.go), [trust classifier](auth.go), [entities](emaistore/emaistore.go); [shared runtime](../chanlib/run.go), [router client](../chanlib/chanlib.go),
[HTTP handlers](../chanlib/handler.go), [store reach](../store/owner_reach.go).

XOAUTH2 rejection behavior still needs a real provider check ([BUGS L425](../BUGS.md)).
