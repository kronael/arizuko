# chanlib

chanlib is the adapter half of the channel protocol: registration, the
service bearer, inbound retention, the delivery ledger, `/health` and the
adapter dashboard. A Go adapter's `main.go` calls `chanlib.Run` with a
`Start` hook and writes only platform code. The registry half is
[`chanreg/README.md`](../chanreg/README.md); which adapter carries a reply is
[ROUTING.md § Reply Routing](../ROUTING.md#reply-routing).

## The channel protocol has four calls

<!-- twin:channel -->

```
adapter, chanlib.Run                   routd
  │ every call carries an ES256 service bearer:
  │ service:<daemon> to routd, service:routd to the adapter
  │                                      │
  │ POST /v1/channels/register           │
  ├─────────────────────────────────────►│ origin pin: the first (IP, sub)
  │ name, url, jid_prefixes, caps        │ owns the name; a later claim
  │◄─────────────────────────────────────┤ from elsewhere is 409
  │ session token                        │
  │                                      │
  │ GET <url>/health, every 30 s         │
  │◄─────────────────────────────────────┤ ok and stale count as healthy;
  ├─────────────────────────────────────►│ 3 failures deregister
  │ ok, stale or 503 disconnected        │
  │                                      │
  │ POST /v1/messages                    │
  ├─────────────────────────────────────►│ chat_jid inside a registered
  │ inbound; held locally on failure     │ prefix, sender inside its
  │ or left on the platform cursor       │ scheme; PutMessage appends
  │                                      │
  │ POST <url>/send, idempotency_key     │
  │◄─────────────────────────────────────┤ the pending bot row is the
  │ deliverOnce: claim, post, record     │ outbox
  ├─────────────────────────────────────►│ 200 sent; 4xx dead-letters;
  │ 200 {id}, 400, 501 or 502            │ 5xx retries at 30 s, doubling
  │                                      │
  │ POST <url>/typing, /<verb>           │
  │◄─────────────────────────────────────┤ only for an advertised
  │ typing TTL: 90% of RUNED_RUN_TIMEOUT │ capability; else typing sends
  │                                      │ nothing and a verb gets 501
```

<!-- /twin -->

Both sides are plain HTTP servers, so neither side holds connection state or
cares about message order on a socket.

- **Outbound has one queue, routd's.** The bot `messages` row with status
  `pending` is the outbox; there is no second table, and chanreg and the
  adapters hold none. The adapter's 200 on `/send` is the acknowledgement.
  The row's stable id travels as `idempotency_key` into the adapter's
  durable ledger, so a re-send after a lost response posts nothing twice.
  Retries stop after ten failures; the row then becomes `failed`,
  the dead letter.
- **Inbound has no adapter queue unless the platform forgets.** A
  cursor-gated adapter (teled, emaid) leaves the
  event on the platform until routd accepts it. An adapter that consumes the
  Text, file and voice sends carry optional `thread_id` and `thread_root`.
  Adapters advertise each supported placement in their capability map.
  `EditRequest.ThreadID` carries the existing thread context for edits.
  The verbs and their capability map: [EXTENDING.md](../EXTENDING.md#each-adapter-advertises-only-the-verbs-it-implements).

  event on arrival (slakd, discd, gitd) holds it in its own store
  (`RetainInbound`). A second queue is a second thing to drift.

- **Capabilities are declared, never probed from platform errors.** routd
  forwards a verb only when the adapter registered its capability
  (`chanreg/README.md` § Capabilities).

Retaining an inbound message writes its due pointer and increments the backlog
count atomically. Acceptance deletes the payload and decrements the count.
The [shared retry policy](../specs/9/21-state-transitions.md#one-retry-policy)
sets each failure's count, deadline and notice. A stopped payload stays held
without a due pointer. The 15-second retry timer reads
only due pointers, sorts them by first receipt and loads at most 50 payloads.
`/health` reads the held count by key, including stopped payloads.
Adapter startup migrates retry state once. Duplicate ingress preserves the episode.
Before a direct send, the adapter checks the exact retained key. A held message
returns its stored failure; only the sweep can replay it. An unreadable retained
state returns `ErrInboundUnavailable` without a delivery attempt.

### Failure facts and claims

`writeBotResult` maps transient and timeout facts to 503 and permanent or
unknown facts to 400. It preserves Retry-After and structured unsupported
hints use 501. `ErrNotDelivered` proves the platform accepted nothing and
releases the claim. Any other create failure becomes `ErrDeliveryUnresolved`
and keeps the claim. A chunked sender releases it only before any chunk lands.

### One dashboard page for every adapter

`NewAdapterMux` mounts `/dash/<name>/` from one renderer in `dash.go`:
overview, connection, stream health, capabilities, then the adapter's own
sections. An adapter adds state only through the optional `DashSectioner`,
whose rows are label and value pairs, never HTML: ten adapters handed raw
markup would drift into ten layouts. The sections read the adapter's
in-memory state and never open its files or tables.

- The status is `disconnected`, `stale` or `ok`, from the same thresholds as
  `/health`. `stale` renders as a warning, not an error: the process is
  alive and the platform link is quiet.
- The capability strip probes each verb with `isStubVerb`, the same check as
  `CapImplReport`, so a Go adapter cannot show a verb it stubs. The
  TypeScript adapter, whapd, declares its list
  (`supportedVerbs`), so it can drift from the code.
- The page trusts the stamped `X-User-*` only behind proxyd's
  `service:proxyd` bearer (`auth.RequireOperator`). Reading the scope header
  alone would let anyone inside the compose network claim the operator page.
- A control appears only where a mechanism exists. whapd's pair form is the
  one control; a missing affordance is absent, never greyed out.

The route to the page is the adapter's own manifest:
`template/services/<adapter>.yaml` declares `/dash/<name>/`, gated by the
adapter's credential, so the route and the hub tile exist exactly when the
adapter runs.

### Inbound media is appended to the transcript and kept

routd's enricher downloads each attachment from the URL the adapter gave:
the adapter's own `/files/<id>` when the platform's URL expires or needs the
bot's token, else the platform's URL. Only a registered adapter origin receives
routd's `service:routd` bearer. Files go under `groups/<folder>/media/<YYYYMMDD>/`;
guarded completion appends an `<attachment …/>` line and any audio transcript
to the stored message. A download waits 45 s for headers, then 30 s plus one
second per 256 KiB of body. The [shared retry policy](../specs/9/21-state-transitions.md#one-retry-policy)
classifies failures and sets the persisted due time. Pending attachment work
postpones turn admission. Terminal failure retains the raw input and appends
the common unavailable notice once. Observed context gets one attempt.

## Public API

- `Run(opts RunOpts)` — adapter main loop: service-token exchange, register, start, serve, deregister on signal
- `RunOpts` — name, router url, listen addr, listen url, prefixes, caps, start hook
- `NewRouterClient(url) *RouterClient` — router calls with automatic service-token auth
- `RouterClient.SetServiceToken(src)` — install service-token source (ES256 JWT from authd)
- `RouterClient.RetainInbound(ctx, st)` — durable inbound for adapters that
  consume their platform event on arrival (slakd, discd, gitd). A failed
  `SendMessage` is retained in the adapter's own store. A 15s sweep
  delivers due input; permanent or exhausted failures remain held without retries.
  Cursor-gated adapters (teled, emaid) must
  NOT wire it — the platform is already their queue (BUGS `L25`).
- `OpenAdapterStore(service) (AdapterStore, error)` — prepares chanlib's
  entities on the adapter's own table prefix on the instance's store server.
  The ONE door, in three pieces: `Chan` is what the mux and `RetainInbound`
  take, `Close` ends what the open holds, and `Driver` is what emaid adds
  `emaistore`'s entities to.
- `PendingInboundCount(ctx, st)` — undelivered inbound backlog; `/health` reports it
- `NewAdapterMux(name, prefixes, bot, isConnected, lastInboundAt, st) *http.ServeMux`
  — standard handler tree: `/send`, `/send-file`, `/send-voice`,
  `/typing`, `/post`, `/like`, `/dislike`, `/delete`, `/forward`,
  `/quote`, `/repost`, `/edit`, `/pin`, `/unpin`, `/health`, `/v1/history`.
  `isConnected` and `lastInboundAt` required (panics on nil).
  Adapters embed `NoVoiceSender`, `NoFileSender`, `NoSocial`, `NoPinSupport` as needed.
- `Auth(next)` — bearer-token middleware (ES256 service:routd verification, open when AUTHD_URL unset)
- `UnsupportedError{Tool, Platform, Hint}` /
  `errors.Is(err, ErrUnsupported)` — typed unsupported error;
  adapters return HTTP 501 with JSON hint. Agent sees
  `unsupported: <tool> on <platform>: <hint>`.
- `ErrInvalidRequest`, `ErrPermanent`, `ErrNotDelivered` — the error classes
  in § Four error classes
- `ClassifyEmoji(emoji) string` — `"dislike"` for 👎 💩 😡 🤬 💔 🤮 😢, `"like"`
  for every other emoji. The raw emoji travels in `InboundMsg.Reaction`.
- `InboundMsg.Reaction string` — raw emoji on synthetic `like`/`dislike` events.
- `InboundMsg.ChatName string` — human-readable channel/group name. Empty for DMs.
- `InboundMsg.IsGroup bool` — chat classification (group/channel vs DM); upserted onto chats.is_group.
- `InboundMsg.Source string` — adapter's channel name; multi-account variants share one JWT, Source disambiguates.
- `BotHandler`, `HistoryProvider`, `NoSocial`, `NoFileSender`, `NoVoiceSender`, `NoPinSupport` — adapter-side interfaces/mixins
- `DashSection`, `DashSectioner` — an adapter's own dashboard sections
- `CapImplReport(b, caps) []string` — drift report: advertised caps vs actual verb stubs
- `SlackJID`, `ParseSlackJID`, `FormatSlackJID` — Slack JID parsing (format: `slack:<workspace>/<kind>/<id>`)
- `TypingRefresher` — presence re-sender with hard TTL
- `AckMark` — the `👀` mark for adapters with no native presence (slakd, gitd): pins the trigger message, persists the mark in `ack_marks`, removes it at turn end, backstop TTL, `Sweep` at boot; `Stop` removes no mark
- `URLCache` — 12-hex LRU short-ID proxy (cap 4096) shared by discd and slakd
- `FileProxyHandler(opts)` — /files/<id> handler with opaque-id→CDN resolution
- `WriteJSON`, `WriteErr`, `Chunk`, `ProxyFile`, `LogMiddleware`
- `ShortHash(s)` — 4-byte hex of sha256(s) for logging sensitive strings
- `CopyDirNoSymlinks`, `CopyFile` — fs utilities
- `EnvOr`, `EnvInt`, `EnvDur`, `EnvBytes`, `MustEnv` — env helpers
- `InboundMsg`, `InboundAttachment`, `SendRequest`, `PostRequest`, etc.

## Health reports one of three states

`/health` returns:

- `503 {status:"disconnected"}` when `isConnected()` is false (platform link down — whapd waiting on QR, slakd `auth.test` failing).
- `200 {status:"stale", last_inbound_at, stale_seconds}` when no inbound within the staleness threshold (5m default, 10m for email). Informational only — a quiet-but-connected channel is normal and must not 503. Real death surfaces through `isConnected()`.
- `200 {status:"ok"}` otherwise.

`pending_inbound` rides on any of the three when the adapter is holding inbound
routd has not accepted yet. It does not change the status: the sweep is working
through the backlog and the platform link itself is fine.

Docker `HEALTHCHECK` marks containers `(unhealthy)` on 503.

## Delivery is claimed before the platform call

routd's pending bot row is the outbox, and the row's stable id is the
idempotency key end to end. `deliverOnce` (`dedup.go`) is the one place an
adapter enforces it:

1. Claim the key in `outbound_dedup` BEFORE the platform call.
2. A key already `sent` returns the recorded platform id and posts nothing.
3. A key still in flight is refused with `ErrDeliveryUnresolved`, and routd
   dead-letters the row. A visible dead letter beats a doubled message: a
   lost response and a failed post look the same from the adapter.
4. An `ErrNotDelivered` failure frees the claim.
   Every other error keeps it.
5. A success records the platform id under the key.

Every store call runs on `context.Background()`. routd's context would cancel
the write that says "delivered" at the moment the platform accepted the post,
and leave the key in flight.

## Storage

chanlib holds NO SQL and no `CREATE TABLE`. Its storage is `chanlib/chanstore`,
which uses `objstore`'s two primitives over adapter-owned entities and derived indexes.

`OpenAdapterStore(service)` is the door and it reaches the same server every
other owner does, under the adapter's own table prefix. The prefix is the
ADAPTER's name, not the account's, so two accounts of one adapter share
these tables — every key carries the chat JID and a JID carries the channel
name (`telegram-rhias:group/1`), so a held inbound and a delivery claim are
already scoped to the account that made them.

That one storage layer serves every adapter, which is why a statement here
would put itself in all of them at once.

| entity            | coordinate                           | why                                                                                                      |
| ----------------- | ------------------------------------ | -------------------------------------------------------------------------------------------------------- |
| `outbound_dedup`  | the idempotency key, as both halves  | every read is a point read and the ledger never drains, so a constant partition would grow without bound |
| `inbound_pending` | one constant, then the message id    | stable message identity; due pointers select retries and one atomic counter serves health                |
| `file_urls`       | the attachment id, as both halves    | every read is a point read by the id in a `/files/<id>` path                                             |
| `ack_marks`       | one constant, then the chat JID      | startup restores marks; per-mark deadlines read exact keys                                               |
| `state_index`     | access path, then due time or marker | retry pointers, exact health count and migration markers                                                 |

The message id is the sort key because id-uniqueness is the guarantee: a Put
under `ItemAbsent` is what keeps a platform's re-delivery of the same event from
resetting the row's backoff. `next_attempt_at` and `first_seen_at` are declared
ordered at `core.TS`'s width, so a value of another width is refused rather than
sorted into the wrong place (BUGS `L97`).

## Dependencies

- `auth` — service-token exchange, ES256 verification, JWKS fetch
- `obs` — OTLP trace injection

## Files

- `run.go` — `Run` + `RunOpts`, service-token exchange
- `chanlib.go` — `RouterClient`, inbound types, `StatusError`, env/http helpers
- `inbound.go` — `RetainInbound`, the re-delivery sweep
- `dedup.go` — `OpenAdapterStore`, `deliverOnce`, the outbound dedup contract
- `chanstore/` — the storage layer: no SQL, `objstore`'s two primitives
- `handler.go` — `NewAdapterMux`, the `BotHandler` interface, health, `CapImplReport`
- `dash.go` — THE adapter dashboard: one renderer with per-adapter
  deltas, mounted by every Go adapter through `NewAdapterMux`, so an adapter
  gets its page with no per-adapter edit at all
- `github_jid.go` — `GitHubJID`, `ParseGitHubJID`
- `auth.go` — `Auth` middleware (ES256 service:routd verification)
- `typing.go` — `TypingRefresher`
- `mark.go` — `AckMark`
- `urlcache.go` — `URLCache`
- `emoji.go` — `ClassifyEmoji`
- `slack_jid.go` — `SlackJID`, `ParseSlackJID`, `FormatSlackJID`
- `fsutil.go` — `CopyDirNoSymlinks`, `CopyFile`
- `httplog.go` — `LogMiddleware`

## Related docs

- `ARCHITECTURE.md` § Message Flow
- `EXTENDING.md`
