# webd

Web channel daemon: SSE hub, slink chat UI, MCP bridges for web topics.

## Purpose

Registers with the router as channel `web` (prefix `web:`). Stores inbound
web messages via the standard `/v1/messages` API so the gateway's poll
loop delivers them into containers. Hosts the slink widget (short-token
chat) and exposes MCP endpoints used by agents running against web JIDs.

## Responsibilities

- Register as channel `web` with caps `send_text` + `typing` (`main.go`).
- Run the SSE hub that fans agent output to subscribers, keyed on
  `folder/topic` (`hub.go`).
- Serve slink pages: public widget, token-scoped chat (`slink.go`, `pages.go`).
- Serve the user MCP bridge for authed sessions (`mcp.go`) and the
  token-only slink-MCP transport at `/slink/<token>/mcp` (`slink_mcp.go`).
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
observable via `store.LatestSource(jid)` returning `web` for recently
delivered messages.

## Files

- `main.go`, `server.go` — wiring + routes
- `hub.go` — SSE fan-out keyed on `folder/topic`
- `channel.go` — gated→webd callbacks (`/send`, `/v1/round_done`)
- `slink.go` — slink chat UI + `POST /slink/<token>` (form/SSE/JSON)
- `slink_mcp.go` — slink-MCP transport at `POST /slink/<token>/mcp`
  (3 tools: send_message, steer, get_round)
- `turn.go` — round-handle endpoints (`/slink/<token>/<id>{,/status,/sse}`)
- `mcp.go` — authed user MCP bridge at `/mcp`
- `api.go`, `pages.go`, `partials.go` — HTTP surface

## Related docs

- `specs/5/J-sse.md` — SSE streams + slink-MCP transport
- `specs/6/3-chat-ui.md`
- `ARCHITECTURE.md` (Web Channel section)
