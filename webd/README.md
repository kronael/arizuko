# webd

Web channel daemon: websocket hub, slink chat UI, MCP bridge for web topics.

## Purpose

Registers with the router as channel `web` (prefix `web:`). Stores inbound
web messages via the standard `/v1/messages` API so the gateway's poll
loop delivers them into containers. Hosts the slink widget (short-token
chat) and exposes MCP endpoints used by agents running against web JIDs.

## Responsibilities

- Register as channel `web` with caps `send_text` + `typing` (`main.go`).
- Run the websocket hub that fans agent output to connected browsers (`hub.go`).
- Serve slink pages: public widget, token-scoped chat (`slink.go`, `pages.go`).
- Serve the MCP bridge that lets agents talk to web topics (`mcp.go`).
- Accept signed header forwards from `proxyd` (`PROXYD_HMAC_SECRET`).

## Entry points

- Binary: `webd/main.go`
- Listen: `$WEBD_LISTEN` (default `:8080`)
- Routes listed in `pages.go`, `api.go`, `slink.go`

## Dependencies

- `chanlib` (router client, env helpers), `core`, `store`

## Configuration

- `WEBD_LISTEN`, `WEBD_URL`, `ROUTER_URL`, `CHANNEL_SECRET`
- `WEB_HOST`, `ASSISTANT_NAME`
- `PROXYD_HMAC_SECRET` — shared with proxyd to verify forwarded headers

## Health signal

`GET /health` returns 200 when registered with the router. Liveness also
observable via connected websocket count and `store.LatestSource(jid)`
returning `web` for recently delivered messages.

## Files

- `main.go` — wiring
- `hub.go`, `channel.go` — websocket fan-out
- `slink.go` — slink public widget, token-scoped chat
- `mcp.go` — MCP bridge
- `api.go`, `pages.go`, `partials.go` — HTTP surface

## Related docs

- `specs/6/3-chat-ui.md`
- `ARCHITECTURE.md` (Web Channel section)
