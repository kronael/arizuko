# chanlib

Shared HTTP + auth primitives used by Go channel adapters.

## Purpose

Common runtime for channel adapters (teled, discd, mastd, bskyd, reditd,
emaid, linkd) and a few other daemons (webd, onbod). Owns the
router-facing side of the channel protocol: registration, signed POSTs,
retry, typing refresh, health endpoint, URL cache, file proxy. Adapters
stay thin — `main.go` calls `chanlib.Run` with a `Start` hook.

## Public API

- `Run(opts RunOpts)` — the adapter main loop: register, start, serve, deregister on signal
- `RunOpts` — name, router url, secret, listen addr, prefixes, caps, start hook
- `NewRouterClient(url, secret) *RouterClient` — signed POST helpers
- `NewAdapterMux(name, secret, prefixes, bot, isConnected, lastInboundAt) *http.ServeMux`
  — standard handler tree: `/send`, `/send-file`, `/send-voice`,
  `/typing`, `/post`, `/like`, `/dislike`, `/delete`, `/forward`,
  `/quote`, `/repost`, `/edit`, `/health`, `/v1/history`. Both
  `isConnected` and `lastInboundAt` are required (panics on nil).
  Adapters lacking voice embed `NoVoiceSender`, lacking files embed
  `NoFileSender` — both return `*UnsupportedError` from the handler.
- `UnsupportedError{Tool, Platform, Hint}` /
  `errors.Is(err, ErrUnsupported)` — typed unsupported error;
  adapters return HTTP 501 with the hint as JSON body when a verb has
  no native primitive on the platform. Surfaced to the agent as
  `unsupported: <tool> on <platform>\nhint: <alt>`.
- `ClassifyEmoji(emoji) string` — `"like"` or `"dislike"` (small
  explicit negative set; everything else, incl. unknown, is `"like"`).
  Used by inbound reaction handling on discd/teled/whapd.
- `InboundMsg.Reaction string` — raw emoji on synthetic
  `like`/`dislike` inbound events.
- `BotHandler`, `HistoryProvider`, `FileSender`, `NoSocial`, `NoFileSender` — adapter-side interfaces
- `TypingRefresher` — presence re-sender with hard TTL
- `URLCache` — single 12-hex LRU short-ID proxy (cap 4096) shared by
  discd/mastd/reditd
- `Auth(secret, next)` — bearer-token middleware
- `WriteJSON`, `WriteErr`, `Chunk`, `ProxyFile`, `LogMiddleware`
- `ShortHash(s)` — short deterministic hash (also used by onbod/webd)
- `CopyDirNoSymlinks`, `CopyFile` — fs utilities (replace dup'd copies in container/gateway)
- `EnvOr`, `EnvInt`, `EnvDur`, `EnvBytes`, `EnvBool`, `MustEnv` — env helpers
- `InboundMsg`, `InboundAttachment`, `SendRequest`, `PostRequest`, etc.

## Health: `disconnected` > `stale` > `ok`

`/health` is `503 {status:"disconnected"}` when `isConnected()` is
false (platform link down — whapd waiting on QR, mastd stream
dropped, …). Otherwise, if no inbound has flowed within the
per-adapter staleness threshold (`5m` realtime, `10m` for emaid),
`503 {status:"stale", last_inbound_at, stale_seconds}`. Otherwise
`200 {status:"ok"}`. Docker `HEALTHCHECK` flips the container to
`(unhealthy)` automatically.

## Dependencies

- `core`

## Files

- `run.go` — `Run` + `RunOpts`
- `chanlib.go` — `RouterClient`, inbound types, env/http helpers
- `handler.go` — `NewAdapterMux`, `BotHandler` interface, health
- `retry.go` — outbound retry policy
- `typing.go` — `TypingRefresher`
- `urlcache.go` — `URLCache`
- `emoji.go` — `ClassifyEmoji`
- `fsutil.go` — `CopyDirNoSymlinks`, `CopyFile`
- `httplog.go` — `LogMiddleware`

## Related docs

- `ARCHITECTURE.md` (Channel Protocol, Inbound Media Pipeline)
- `EXTENDING.md`
- `specs/4/1-channel-protocol.md`
