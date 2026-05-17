# api

Channel adapter HTTP API. Mounted by `gated`.

## Purpose

The `/v1/channels/*` and `/v1/messages` surface that every channel
adapter (teled, whapd, mastd, ...) calls into. Adapters register here
to receive a session token, POST inbound messages, and receive outbound
sends through their own callback URL. This is the **router-side** of
the channel protocol — the adapter-side helpers live in `chanlib/`.

Hosted as a single `*Server`; gated mounts `Handler()` on the same
listener as `/health` and the other federated `/v1/*` resources owned
by gated.

## Public API

- `New(reg *chanreg.Registry, s *store.Store) *Server`
- `(*Server).Handler() http.Handler` — net/http mux for all routes
- `(*Server).OnRegister(fn func(name string, ch *chanreg.HTTPChannel))`
  — hook fired after a successful register so gated can wire the new
  channel into the outbound dispatcher
- `(*Server).OnDeregister(fn func(name string))`
- `(*Server).ChannelLookup(fn func(name string) *chanreg.HTTPChannel)`
  — optional override for outbound resolution (tests, multi-tenant
  channel wrappers)

## HTTP routes

| Method + path                  | Auth               | Behaviour                                                                                         |
| ------------------------------ | ------------------ | ------------------------------------------------------------------------------------------------- |
| `POST /v1/channels/register`   | `CHANNEL_SECRET`   | Allocates an adapter session token, pins origin IP + secret to prevent name hijack                |
| `POST /v1/channels/deregister` | Bearer session tok | Removes the adapter from the registry, fires `OnDeregister`                                       |
| `POST /v1/messages`            | Bearer session tok | Inbound; stamps `source = adapter name`, persists via `store.PutMessage`, may consume a link code |
| `POST /v1/outbound`            | `CHANNEL_SECRET`   | Outbound send; resolves channel by `req.Channel` → `LatestSource(jid)` → `Resolve(name, jid)`     |
| `GET  /v1/channels`            | `CHANNEL_SECRET`   | List registered adapters as `channelDTO` (no token leakage)                                       |
| `GET  /health`, `GET /ready`   | none               | Liveness                                                                                          |

Request bodies are capped: 25 MiB on `/v1/messages` (inline base64
attachments from whapd), 10 MiB everywhere else.

## Notable behaviours

- **Origin pin** — `RegisterWithOrigin` records the source IP and the
  presented `CHANNEL_SECRET` at first registration; re-registers from a
  different IP fail. Blocks lateral hijack by another secret holder.
- **JID prefix ownership** — `entry.Owns(req.ChatJID)` rejects any
  inbound whose JID doesn't match the adapter's declared prefixes.
  Stops one adapter from forging traffic on another's platform.
- **Link-code consumption** — bare-content messages matching
  `^link-[0-9a-f]{12}$` invoke `store.ConsumeLinkCode` so users can
  bind a channel identity from chat. Side effect only; never blocks
  delivery.
- **Outbound resolution order** — explicit `channel` field →
  `store.LatestSource(jid)` → `reg.Resolve(name, jid)`. Lets the agent
  override the canonical-source heuristic when it knows better.

## Dependencies

- `chanlib`, `chanreg`, `core`, `store`

## Configuration

Inherits from gated. Relevant vars: `CHANNEL_SECRET` (auth for the
register/list/outbound endpoints).

## Files

- `api.go` — server + all handlers
- `api_test.go`, `identities_test.go`

## Related docs

- `ARCHITECTURE.md` (Channel Protocol)
- `../chanlib/README.md` — adapter-side client
- `../chanreg/README.md` — in-memory registry + health loop
- `specs/4/1-channel-protocol.md` — wire contract
