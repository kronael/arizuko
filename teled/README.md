# teled

teled connects a Telegram bot to routd. It owns the polling offset and the `teled.*` adapter entities.
Telegram holds inbound updates until teled advances the offset after routd accepts them.

[Architecture](../ARCHITECTURE.md) covers the system; [channel protocol](../chanlib/README.md) defines the shared wire format.

```text
Telegram Bot API ──► teled ──► routd:8080
routd ──► teled:8080 ──► Telegram Bot API
teled ──► authd:8080 and the store
```

## HTTP interfaces

| Method and path                                                                                          | Caller and behavior                                                           |
| -------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `POST /send`, `/send-file`                                                                               | routd; text or multipart file delivery with an optional `idempotency_key`.    |
| `POST /send-voice`, `/typing`                                                                            | routd; audio upload or activity indicator.                                    |
| `POST /post`, `/like`, `/delete`, `/forward`, `/quote`, `/repost`, `/dislike`, `/edit`, `/pin`, `/unpin` | routd; platform actions. Unsupported actions return 501.                      |
| `GET /health`                                                                                            | Public platform status and pending inbound count.                             |
| `GET /dash/<daemon>/`                                                                                    | Operator page; compose supplies the daemon name through `AUTHD_SERVICE_NAME`. |
| `GET /files/<file-id>`                                                                                   | routd bearer; resolves Telegram `getFile` and streams the attachment.         |

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

`StoreReach["teled"]` opens only `teled`. No routd table is opened directly.
`DATA_DIR/teled-offset-<CHANNEL_NAME>` stores the next update ID through a synced temporary file and rename.
An unreadable offset file aborts startup. An offset save failure logs a warning; three consecutive failures exit the process.
The file is independent of the remote adapter store. Mount the directory across container recreation.

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

| Variable                                     | Code default                | Meaning                                                                                          |
| -------------------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------------ |
| `TELEGRAM_BOT_TOKEN`                         | required                    | Telegram bot credential; startup calls the Bot API.                                              |
| `ROUTER_URL`                                 | required                    | routd base URL, normally `http://routd:8080`.                                                    |
| `CHANNEL_NAME`                               | `telegram`                  | Registration name and offset-file suffix.                                                        |
| `LISTEN_ADDR`                                | `:9001`                     | Code listener; compose uses `:8080`.                                                             |
| `LISTEN_URL`                                 | `http://telegram:9001`      | Advertised address; compose uses `http://teled:8080`.                                            |
| `DATA_DIR`                                   | `/srv/app/home/store/teled` | Directory for the polling offset.                                                                |
| `ASSISTANT_NAME`                             | empty                       | Mention detection and bot-name replacement.                                                      |
| `MEDIA_MAX_FILE_BYTES`                       | `20971520`                  | Maximum proxied download size in bytes.                                                          |
| `AUTHD_URL`                                  | required                    | authd URL, normally `http://authd:8080`; JWKS and service-token exchange.                        |
| `AUTHD_SERVICE_KEY`                          | required                    | Bootstrap secret exchanged for a service JWT.                                                    |
| `AUTHD_SERVICE_NAME`                         | channel name                | Exchange as the base daemon name; compose sets this explicitly. Also selects the dashboard path. |
| `DYNAMODB_URL`                               | required                    | Store endpoint; tables use the daemon's fixed prefix.                                            |
| `AWS_REGION`                                 | `us-east-1`                 | Store signing region.                                                                            |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | required                    | This daemon's store identity.                                                                    |
| `STORE_CA`                                   | unset                       | Base64 PEM certificate for the store endpoint; unset uses system trust.                          |
| `ARIZUKO_INSTANCE`                           | unset                       | Observability instance label.                                                                    |
| `LOG_LEVEL`                                  | `info`                      | JSON stderr log level: debug, info, warn, error.                                                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                | unset                       | Optional OTLP log export; see [obs](../obs/README.md).                                           |

## Loops and failure diagnosis

The adapter registers `telegram:<bot-id>/` and `telegram:group/`.
`getUpdates` uses a 30 s long poll. A failed poll sets disconnected and waits 3 s before retrying.
A failed routd delivery stops the offset from advancing; Telegram supplies the update again.
Typing refreshes every 5 s with a 10 minute maximum lifetime.
Connected inbound silence over 5 minutes reports `stale` with HTTP 200.
Grep `telegram auth failed`, `getUpdates failed`, `offset save failed`, and `adapter store open failed`.
A failed initial Telegram authentication exits before the HTTP listener starts.

## Limits and verification

Advertised verbs: text, file, voice, typing, post, forward, edit, reaction, delete, pin, buttons, and thread IDs.
`quote`, `repost`, and `dislike` return hints. Use a negative emoji with `like`.
The Bot API supplies no history fetch; routd serves stored history.
A task targeting a permanently dead Telegram chat still requires an operator decision ([BUGS L159](../BUGS.md)).
Bot API behavior and live credentials are not verified by local documentation checks.

Sources: [configuration](main.go), [polling and actions](bot.go), [file proxy](server.go); [shared runtime](../chanlib/run.go), [router client](../chanlib/chanlib.go),
[HTTP handlers](../chanlib/handler.go), [store reach](../store/owner_reach.go).
