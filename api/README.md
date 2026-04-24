# api

Router-side HTTP API. Imported by `gated`.

## Purpose

HTTP surface that channel adapters register against. Stamps inbound
messages with `source` (canonical adapter of record), issues/revokes
session tokens, dispatches outbound to the right `HTTPChannel`.

## Public API

- `New(reg *chanreg.Registry, s *store.Store) *Server`
- `(*Server).Handler() http.Handler`
- `(*Server).OnRegister(fn)`, `OnDeregister(fn)`, `ChannelLookup(fn)`

## HTTP routes

- `POST /v1/channels/register` — auth required; returns session token
- `POST /v1/channels/deregister` — tokened
- `POST /v1/messages` — inbound; stamps `source`, writes via store
- `POST /v1/outbound` — auth required; routes to `HTTPChannel` by name
- `GET  /v1/channels` — list
- `GET  /health`, `GET /ready`

## Dependencies

- `chanreg`, `core`, `store`

## Files

- `api.go`

## Related docs

- `ARCHITECTURE.md` (Channel Protocol)
- `specs/4/1-channel-protocol.md`
