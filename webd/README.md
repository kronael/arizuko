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

## Tables owned

Per `specs/6/R-platform-api.md`: `web_routes`, `vhosts`, slink tokens.
webd does not own messages — it writes them as a client of
`gated/v1/messages` (channel adapter inbound) and today reads them via
its own `/api/*` paths directly off the shared DB. Once federation lands
those reads migrate to `gated/v1/messages` (flagged future work).

## Surface

Shipped today (see `server.go`):

- `POST /send`, `POST /typing`, `POST /v1/round_done` — channel adapter
  inbound from gated, signed via `CHANNEL_SECRET` (`channel.go`).
- `GET /api/groups`, `GET /api/groups/{folder}/topics`,
  `GET /api/groups/{folder}/messages` — chat UI JSON reads
  (`api.go`).
- `GET /x/*` — HTMX partials (`partials.go`).
- `GET|POST /slink/*` — public unauthenticated guest surface
  (`slink.go`, `slink_mcp.go`, `turn.go`).
- `POST|GET|DELETE /mcp` — authed user MCP bridge (`mcp.go`).
- `GET /static/*` — embedded assets.
- `GET /health`.

Planned per `specs/6/R-platform-api.md`:

- `/v1/web-routes` and `/v1/vhosts` — REST verbs on owned tables.

## Token contract

- `/v1/*` endpoints (current and planned) verify JWTs via
  `auth.VerifyHTTP`; `auth.Mint` issuance lives at proxyd / MCP host /
  onbod, never here.
- `/send`, `/typing`, `/v1/round_done` keep their existing
  `chanlib.Auth(channelSecret)` HMAC contract.
- `/api/*`, `/x/*`, `/chat/*`, `/mcp` keep `requireUser` /
  `requireFolder` middleware over headers signed by `proxyd`
  (`PROXYD_HMAC_SECRET`).
- `/slink/*` stays unauthenticated; the URL token IS the capability.

## Future work

webd's `/api/*` chat reads currently hit the shared DB directly. Once
gated ships `/v1/messages` per the platform API spec, migrate these
reads to gated; webd then owns only `web_routes`, `vhosts`, and slink
tokens end-to-end.

## Entry points

- Binary: `webd/main.go`
- Listen: `$WEBD_LISTEN` (default `:8080`)
- Routes wired in `server.go`

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
  (2 tools: send_message, get_round)
- `turn.go` — round-handle endpoints (`/slink/<token>/<id>{,/status,/sse}`)
- `mcp.go` — authed user MCP bridge at `/mcp`
- `api.go`, `pages.go`, `partials.go` — HTTP surface

## Related docs

- `specs/6/R-platform-api.md` — federated `/v1/*` contract
- `specs/5/J-sse.md` — SSE streams + slink-MCP transport
- `specs/6/3-chat-ui.md`
- `ARCHITECTURE.md` (Web Channel section)
