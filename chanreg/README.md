# chanreg

Channel registry + HTTP channel proxy.

## Purpose

Tracks registered channel adapters by name with their URL, JID prefixes,
capabilities, and session token. Runs a health loop that pings each
adapter's `/health` every 30s; 3 consecutive failures auto-deregister.
`HTTPChannel` implements `core.Channel` by POSTing to `<url>/send` and
buffers outbound while an adapter is disconnected.

## Public API

- `New(secret string) *Registry`
- `(*Registry).Register(name, url, prefixes, caps) (*Entry, string, error)`
- `(*Registry).Deregister(name)`, `Get(name)`, `ByToken(token)`, `All()`, `ForJID(jid)`
- `(*Registry).StartHealthLoop(ctx)`, `CheckAll(ctx)`
- `NewHTTPChannel(e *Entry, secret string) *HTTPChannel`
- `(*HTTPChannel).Send`, `SendFile`, `Typing`, `DrainOutbox`, `Owns`, `Connect`, `Disconnect`
- `Entry` — per-adapter row

## Dependencies

- `core`

## Files

- `chanreg.go` — Registry, Entry, lookup
- `health.go` — periodic health pinger
- `httpchan.go` — `HTTPChannel` (implements `core.Channel` over HTTP)

## Related docs

- `ARCHITECTURE.md` (Channel Protocol)
- `specs/4/1-channel-protocol.md`
