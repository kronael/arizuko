# googd

googd polls Google Drive comment threads and sends replies through a service account. It owns `googd.*` adapter state and file cursors.
It reads routd's `gdrive_files` bindings directly and exposes authenticated Drive and Sheets HTTP passthroughs.

[Architecture](../ARCHITECTURE.md) covers the system; [channel protocol](../chanlib/README.md) defines the shared wire format.

```text
Google Drive comments ──► googd ──► routd:8080
routd ──► googd:8080 ──► Google Drive replies
connector calls ──► googd /google/ ──► Drive or Sheets
googd ──► authd:8080 and the store
```

## HTTP interfaces

| Method and path                                                                                          | Caller and behavior                                                                           |
| -------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `POST /send`, `/send-file`                                                                               | routd; text or multipart file delivery with an optional `idempotency_key`.                    |
| `POST /send-voice`, `/typing`                                                                            | routd; audio upload or activity indicator.                                                    |
| `POST /post`, `/like`, `/delete`, `/forward`, `/quote`, `/repost`, `/dislike`, `/edit`, `/pin`, `/unpin` | routd; platform actions. Unsupported actions return 501.                                      |
| `GET /health`                                                                                            | Public platform status and pending inbound count.                                             |
| `GET /dash/<daemon>/`                                                                                    | Operator page; compose supplies the daemon name through `AUTHD_SERVICE_NAME`.                 |
| `/google/drive/`, `/google/sheets/`                                                                      | Connector bearer; API passthrough using separate Google scopes. Not published through proxyd. |

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

`StoreReach["googd"]` opens `routd` and `googd`.
`routd.gdrive_files` binds file IDs to target folders; routd owns management through `/v1/gdrive_files`.
`googd.file_poll` holds each file's cursor, name, status, and last poll time, partitioned by channel name.
The cursor advances only after routd accepts the fetched comments and the store saves the status.
A save failure restores the in-memory cursor. googd owns no persistent local files.

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

| Variable                                     | Code default                    | Meaning                                                                                          |
| -------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------ |
| `CHANNEL_NAME`                               | `gdrive`                        | Registration name and cursor partition.                                                          |
| `GDRIVE_SA_KEY_JSON`                         | unset                           | Service-account JSON itself; missing or malformed JSON disables API work.                        |
| `GDRIVE_IMPERSONATE`                         | unset                           | User subject for delegated service-account authorization.                                        |
| `GDRIVE_API_URL`                             | `https://www.googleapis.com`    | Drive API base URL.                                                                              |
| `SHEETS_API_URL`                             | `https://sheets.googleapis.com` | Sheets API base URL.                                                                             |
| `GOOGD_CONNECTOR_TOKEN`                      | unset                           | Bearer required by `/google/`; unset refuses every passthrough request.                          |
| `GDRIVE_POLL_INTERVAL`                       | `30s`                           | Go duration, minimum 10 s.                                                                       |
| `ROUTER_URL`                                 | required                        | routd base URL.                                                                                  |
| `LISTEN_ADDR`                                | `:8080`                         | HTTP listener.                                                                                   |
| `LISTEN_URL`                                 | `http://googd:8080`             | Address advertised to routd.                                                                     |
| `AUTHD_URL`                                  | required                        | authd URL, normally `http://authd:8080`; JWKS and service-token exchange.                        |
| `AUTHD_SERVICE_KEY`                          | required                        | Bootstrap secret exchanged for a service JWT.                                                    |
| `AUTHD_SERVICE_NAME`                         | channel name                    | Exchange as the base daemon name; compose sets this explicitly. Also selects the dashboard path. |
| `DYNAMODB_URL`                               | required                        | Store endpoint; tables use the daemon's fixed prefix.                                            |
| `AWS_REGION`                                 | `us-east-1`                     | Store signing region.                                                                            |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | required                        | This daemon's store identity.                                                                    |
| `STORE_CA`                                   | unset                           | Base64 PEM certificate for the store endpoint; unset uses system trust.                          |
| `ARIZUKO_INSTANCE`                           | unset                           | Observability instance label.                                                                    |
| `LOG_LEVEL`                                  | `info`                          | JSON stderr log level: debug, info, warn, error.                                                 |
| `OTEL_EXPORTER_OTLP_ENDPOINT`                | unset                           | Optional OTLP log export; see [obs](../obs/README.md).                                           |

## Loops and failure diagnosis

The file poll runs immediately and then every `GDRIVE_POLL_INTERVAL`.
A 429, 5xx, or 403 `userRateLimitExceeded` waits 30 s, doubling to 15 minutes per file.
Other failures leave the cursor unchanged for the next ordinary poll.
Drive and Sheets credential/API probes run every 30 s when the key can construct a token source.
Google calls use a 15 s timeout. A recovered Drive probe restores send, edit, and delete capabilities.
A malformed or missing key requires configuration repair and restart; the process keeps serving its dashboard.
`/health` reports process availability even when the credential is unusable. Check API rows on `/dash/googd/`.
Grep `service account credential unusable`, `read Drive bindings`, `poll Drive file`, and `save Drive status`.

## Limits and verification

The `gdrive:` channel advertises text, edit, and delete only after a successful Drive probe.
Replies require a thread JID. Root-comment editing, resolving, and reopening belong to connector calls.
There is no push receiver. Polling determines inbound delivery.
Passthrough can access every file the service account can access; connector grants must bound caller access.
Tests use fake Google endpoints; live sharing, scopes, and delegated identity require an operator check.

Sources: [entry point and actions](main.go), [tokens and probes](token.go), [polling](poll.go), [passthrough](passthrough.go); [shared runtime](../chanlib/run.go), [router client](../chanlib/chanlib.go),
[HTTP handlers](../chanlib/handler.go), [store reach](../store/owner_reach.go).
