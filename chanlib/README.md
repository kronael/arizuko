# chanlib

Shared HTTP + auth primitives used by Go channel adapters.

## Purpose

Common runtime for channel adapters (teled, discd, bskyd, reditd,
emaid, linkd, whapd, slakd, twitd) and a few other daemons (webd, onbod). Owns the
router-facing side of the channel protocol: registration, JWT-signed POSTs,
retry, typing refresh, health endpoint, URL cache, file proxy. Adapters
stay thin — `main.go` calls `chanlib.Run` with a `Start` hook.

## Public API

- `Run(opts RunOpts)` — adapter main loop: service-token exchange, register, start, serve, deregister on signal
- `RunOpts` — name, router url, listen addr, listen url, prefixes, caps, start hook
- `NewRouterClient(url) *RouterClient` — router calls with automatic service-token auth
- `RouterClient.SetServiceToken(src)` — install service-token source (ES256 JWT from authd)
- `RouterClient.RetainInbound(ctx, st)` — durable inbound for adapters that
  consume their platform event on arrival (slakd, discd, gitd). A failed
  `SendMessage` is retained in the adapter's own store and a 15s sweep
  re-delivers it; a routd REFUSAL (400/403/413/422) is dropped with an ERROR
  line instead. Cursor-gated adapters (teled, bskyd, reditd, linkd, emaid) must
  NOT wire it — the platform is already their queue. Spec `5/34`, BUGS `L25`.
- `OpenAdapterDB(dataDir, service) (*sql.DB, *chanstore.Store)` — opens
  `<dataDir>/<service>.db` and prepares chanlib's entities. The ONE door: the
  store is what the mux and `RetainInbound` take, the pool is the engine handle
  the adapter closes and emaid adds `emaistore`'s entities to.
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
- `ErrInvalidRequest` — caller-input error (bad emoji name, missing target, malformed id); maps to 400
- `ClassifyEmoji(emoji) string` — `"like"` or `"dislike"` (explicit negative set; unknown → `"like"`).
- `InboundMsg.Reaction string` — raw emoji on synthetic `like`/`dislike` events.
- `InboundMsg.ChatName string` — human-readable channel/group name. Empty for DMs.
- `InboundMsg.IsGroup bool` — chat classification (group/channel vs DM); upserted onto chats.is_group.
- `InboundMsg.Source string` — adapter's channel name; multi-account variants share one JWT, Source disambiguates.
- `BotHandler`, `HistoryProvider`, `NoSocial`, `NoFileSender`, `NoVoiceSender`, `NoPinSupport` — adapter-side interfaces/mixins
- `CapImplReport(b, caps) []string` — drift report: advertised caps vs actual verb stubs
- `SlackJID`, `ParseSlackJID`, `FormatSlackJID` — Slack JID parsing (format: `slack:<workspace>/<kind>/<id>`)
- `TypingRefresher` — presence re-sender with hard TTL
- `URLCache` — 12-hex LRU short-ID proxy (cap 4096) shared by discd/reditd
- `FileProxyHandler(opts)` — /files/<id> handler with opaque-id→CDN resolution
- `WriteJSON`, `WriteErr`, `Chunk`, `ProxyFile`, `LogMiddleware`
- `ShortHash(s)` — 4-byte hex of sha256(s) for logging sensitive strings
- `CopyDirNoSymlinks`, `CopyFile` — fs utilities
- `EnvOr`, `EnvInt`, `EnvDur`, `EnvBytes`, `MustEnv` — env helpers
- `InboundMsg`, `InboundAttachment`, `SendRequest`, `PostRequest`, etc.

## Health: `disconnected` > `stale` > `ok`

`/health` returns:

- `503 {status:"disconnected"}` when `isConnected()` is false (platform link down — whapd waiting on QR, slakd `auth.test` failing).
- `200 {status:"stale", last_inbound_at, stale_seconds}` when no inbound within the staleness threshold (5m default, 10m for email, 60m for reddit). Informational only — a quiet-but-connected channel is normal and must not 503. Real death surfaces through `isConnected()`.
- `200 {status:"ok"}` otherwise.

`pending_inbound` rides on any of the three when the adapter is holding inbound
routd has not accepted yet. It does not change the status: the sweep is working
through the backlog and the platform link itself is fine.

Docker `HEALTHCHECK` marks containers `(unhealthy)` on 503.

## Storage

chanlib holds NO SQL and no `CREATE TABLE`; `db_utils/sql_layer_test.go` fails
the build if a statement appears anywhere under `chanlib/`. Its storage is
`chanlib/chanstore`, which is `objstore`'s two primitives over two entities.
They land in EIGHT adapter databases — one per channel, plus `emaid.db` — so a
statement here would put itself in all of them at once.

| entity            | coordinate                          | why                                                                                                      |
| ----------------- | ----------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `outbound_dedup`  | the idempotency key, as both halves | every read is a point read and the ledger never drains, so a constant partition would grow without bound |
| `inbound_pending` | one constant, then the message id   | the sweep and the health count both ask about the WHOLE queue, and the queue drains                      |

The message id is the sort key because id-uniqueness is the guarantee: a Put
under `ItemAbsent` is what keeps a platform's re-delivery of the same event from
resetting the row's backoff. `next_attempt_at` and `first_seen_at` are declared
ordered at `core.TS`'s width, so a value of another width is refused rather than
sorted into the wrong place (BUGS `L97`).

`arizuko migrate-chanlib <instance>` moves every adapter database under an
instance's store onto those entities, setting the old tables aside as `_v1`.
`--dry-run` reads and checks without writing; `--db <path>` does one file.

## Dependencies

- `auth` — service-token exchange, ES256 verification, JWKS fetch
- `obs` — OTLP trace injection

## Files

- `run.go` — `Run` + `RunOpts`, service-token exchange
- `chanlib.go` — `RouterClient`, inbound types, `StatusError`, env/http helpers
- `inbound.go` — `RetainInbound`, the re-delivery sweep
- `dedup.go` — `OpenAdapterDB`, `deliverOnce`, the outbound dedup contract
- `chanstore/` — the storage layer: no SQL, `objstore`'s two primitives
- `handler.go` — `NewAdapterMux`, `BotHandler` interface, health, `CapImplReport`
- `auth.go` — `Auth` middleware (ES256 service:routd verification)
- `retry.go` — outbound retry policy
- `typing.go` — `TypingRefresher`
- `urlcache.go` — `URLCache`
- `emoji.go` — `ClassifyEmoji`
- `slack_jid.go` — `SlackJID`, `ParseSlackJID`, `FormatSlackJID`
- `fsutil.go` — `CopyDirNoSymlinks`, `CopyFile`
- `httplog.go` — `LogMiddleware`

## Related docs

- `ARCHITECTURE.md` (Channel Protocol, Inbound Media Pipeline)
- `EXTENDING.md`
- `specs/5/34-channel-protocol.md`
