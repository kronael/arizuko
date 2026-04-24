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
- `NewAdapterMux(name, secret, prefixes, bot, isConnected, lastInboundAt)` — standard handler tree (`/send`, `/send-file`, `/typing`, `/post`, `/react`, `/delete`, `/health`, `/v1/history`)
- `BotHandler`, `HistoryProvider`, `FileSender`, `NoSocial`, `NoFileSender` — adapter-side interfaces
- `TypingRefresher` — presence re-sender with hard TTL
- `URLCache` — short-ID proxy for CDN URLs (discd, mastd, reditd)
- `Auth(secret, next)` — bearer-token middleware
- `WriteJSON`, `WriteErr`, `Chunk`, `ProxyFile`, `LogMiddleware`
- `ShortHash(s)` — short deterministic hash
- `CopyDirNoSymlinks`, `CopyFile` — fs utilities
- `EnvOr`, `EnvInt`, `EnvDur`, `EnvBytes`, `MustEnv` — env helpers
- `InboundMsg`, `InboundAttachment`, `SendRequest`, `PostRequest`, etc.

## Dependencies

- `core`

## Files

- `run.go` — `Run` + `RunOpts`
- `chanlib.go` — `RouterClient`, inbound types, env/http helpers
- `handler.go` — `NewAdapterMux`, `BotHandler` interface, health
- `retry.go` — outbound retry policy
- `typing.go` — `TypingRefresher`
- `urlcache.go` — `URLCache`
- `fsutil.go` — `CopyDirNoSymlinks`, `CopyFile`
- `httplog.go` — `LogMiddleware`

## Related docs

- `ARCHITECTURE.md` (Channel Protocol, Inbound Media Pipeline)
- `EXTENDING.md`
- `specs/4/1-channel-protocol.md`
