# discd

discd connects Discord gateway events and REST actions to routd. It owns `discd.*` delivery, attachment, and retention entities.
The gateway offers no adapter replay cursor, so discd retains failed inbound deliveries in its own store.

[Architecture](../ARCHITECTURE.md) covers the system; [channel protocol](../chanlib/README.md) defines the shared wire format.

```text
Discord gateway ──► discd ──► routd:8080
routd ──► discd:8080 ──► Discord REST
discd ──► authd:8080 and the store
```

## HTTP interfaces

| Method and path                                                                                          | Caller and behavior                                                           |
| -------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `POST /send`, `/send-file`                                                                               | routd; text or multipart file delivery with an optional `idempotency_key`.    |
| `POST /send-voice`, `/typing`                                                                            | routd; audio upload or activity indicator.                                    |
| `POST /post`, `/like`, `/delete`, `/forward`, `/quote`, `/repost`, `/dislike`, `/edit`, `/pin`, `/unpin` | routd; platform actions. Unsupported actions return 501.                      |
| `GET /health`                                                                                            | Public platform status and pending inbound count.                             |
| `GET /dash/<daemon>/`                                                                                    | Operator page; compose supplies the daemon name through `AUTHD_SERVICE_NAME`. |
| `GET /files/<id>`                                                                                        | routd bearer; resolves `file_urls` and proxies the Discord attachment.        |
| `GET /v1/history?jid=<jid>&before=<RFC3339>&limit=<n>`                                                   | routd bearer; Discord history. Default limit 100.                             |

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

`StoreReach["discd"]` opens only `discd`. The persistent attachment mapping survives restarts.
Multipart uploads use temporary files; discd owns no persistent workspace directory.
JIDs are `discord:<guild>/<channel>` or `discord:dm/<channel>`.

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

| Variable                                     | Code default          | Meaning                                                                                          |
| -------------------------------------------- | --------------------- | ------------------------------------------------------------------------------------------------ |
| `DISCORD_TOKEN`                              | required              | `Bot <token>` for a bot; a bare token selects user mode.                                         |
| `ROUTER_URL`                                 | required              | routd base URL.                                                                                  |
| `CHANNEL_NAME`                               | `discord`             | Registration name; prefix stays `discord:`.                                                      |
| `LISTEN_ADDR`                                | `:9002`               | Code listener; compose uses `:8080`.                                                             |
| `LISTEN_URL`                                 | `http://discord:9002` | Advertised address; compose uses `http://discd:8080`.                                            |
| `ASSISTANT_NAME`                             | empty                 | Name substituted for bot mentions in inbound text.                                               |
| `MEDIA_MAX_FILE_BYTES`                       | `20971520`            | Maximum proxied attachment size in bytes.                                                        |
| `AUTHD_URL`                                  | required              | authd URL, normally `http://authd:8080`; JWKS and service-token exchange.                        |
| `AUTHD_SERVICE_KEY`                          | required              | Bootstrap secret exchanged for a service JWT.                                                    |
| `AUTHD_SERVICE_NAME`                         | channel name          | Exchange as the base daemon name; compose sets this explicitly. Also selects the dashboard path. |
| `DYNAMODB_URL`                               | required              | Store endpoint; tables use the daemon's fixed prefix.                                            |
| `AWS_REGION`                                 | `us-east-1`           | Store signing region.                                                                            |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | required              | This daemon's store identity.                                                                    |
| `STORE_CA`                                   | unset                 | Base64 PEM certificate for the store endpoint; unset uses system trust.                          |
| `ARIZUKO_INSTANCE`                           | unset                 | Observability instance label.                                                                    |
| `LOG_LEVEL`                                  | `info`                | JSON stderr log level: debug, info, warn, error.                                                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                | unset                 | Optional OTLP log export; see [obs](../obs/README.md).                                           |

## Loops and failure diagnosis

Inbound retention sweeps every 15 s, up to 50 due rows per sweep.
The first retry waits 30 s; later failures double the wait to a 15 minute maximum.
Responses 400, 403, 413, and 422 are permanent refusals and drop the row with an error log.
Network faults, 401, 429, and 5xx retain the row.
If the router and retention store both fail, no durable queue holds that event.
Grep `inbound retained for re-delivery`, `retained inbound still undelivered`, and `inbound retain failed`.

Discordgo manages gateway reconnection. A disconnected gateway makes `/health` return 503.
Typing refreshes every 4 s and stops after 10 minutes.
Connected inbound silence over 5 minutes reports `stale` with HTTP 200.
Grep `discord auth failed`, `discord connect failed`, and `adapter store open failed`.
Group mentions and replies to the bot carry `mention`; other group messages carry `message`.
Text sends split at 2000 characters. A thread send can create a thread from `thread_root`.

## Limits and verification

Advertised verbs: text, file, voice, typing, history, edit, quote, delete, pin, reaction, buttons, thread root, and thread ID.
Forward, post, repost, and dislike return 501 hints. Bulk unpin returns a hint.
User mode omits gateway intents. Platform permissions and live reconnection behavior need a real Discord account.
Per-chat session partitioning remains a design question ([BUGS L86](../BUGS.md)).

Sources: [entry point](main.go), [gateway and actions](bot.go), [file proxy](server.go), [retention](../chanlib/inbound.go); [shared runtime](../chanlib/run.go), [router client](../chanlib/chanlib.go),
[HTTP handlers](../chanlib/handler.go), [store reach](../store/owner_reach.go).
