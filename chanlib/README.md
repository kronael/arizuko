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
- `RouterClient.RetainInbound(ctx, db)` — durable inbound for adapters that
  consume their platform event on arrival (slakd, discd). A failed
  `SendMessage` lands in the adapter's `inbound_pending` table and a 15s sweep
  re-delivers it; a routd REFUSAL (400/403/413/422) is dropped with an ERROR
  line instead. Cursor-gated adapters (teled, bskyd, reditd, linkd, emaid) must
  NOT wire it — the platform is already their queue. Spec `5/34`, BUGS `L25`.
- `OpenAdapterDB(dataDir, service) *sql.DB` — opens `<dataDir>/<service>.db` and
  applies chanlib's schema (`outbound_dedup`, `inbound_pending`)
- `PendingInboundCount(db)` — undelivered inbound backlog; `/health` reports it
- `NewAdapterMux(name, prefixes, bot, isConnected, lastInboundAt, db) *http.ServeMux`
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

## Dependencies

- `auth` — service-token exchange, ES256 verification, JWKS fetch
- `obs` — OTLP trace injection

## Files

- `run.go` — `Run` + `RunOpts`, service-token exchange
- `chanlib.go` — `RouterClient`, inbound types, `StatusError`, env/http helpers
- `inbound.go` — `RetainInbound`, the `inbound_pending` sweep
- `dedup.go` — `OpenAdapterDB`, the `outbound_dedup` claim/settle ledger
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
