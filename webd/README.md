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
- Serve guest chat pages: public widget, token-scoped chat at
  `/chat/<token>/` (`route_token.go`, `pages.go`).
- Serve the user MCP bridge for authed sessions (`mcp.go`) and the
  token-only chat-MCP transport at `/chat/<token>/mcp` (`chat_mcp.go`).
- Accept signed header forwards from `proxyd` (`PROXYD_HMAC_SECRET`).

## Tables owned

Per `specs/5/5-uniform-mcp-rest.md`: `web_routes` and the `route_tokens`
table (created in migration `0059-route-tokens.sql`, which also dropped
the legacy `groups.slink_token` column). Per-world hostnames are derived
by proxyd (`<world>.<HOSTING_DOMAIN>` → `/pub/<world>/`), not stored
(`specs/5/V-web-vhosts.md`); webd has no vhost config. webd does not own
messages — it writes them as a client of `routd/v1/messages` (channel
adapter inbound) and today reads them via its own `/api/*` paths directly
off the shared DB. Once federation lands those reads migrate to
`routd/v1/messages` (flagged future work).

## Surface

Shipped today (see `server.go`):

- `POST /send`, `POST /typing`, `POST /v1/round_done` — channel adapter
  inbound from gated, signed via `CHANNEL_SECRET` (`channel.go`).
- `GET /api/groups`, `GET /api/groups/{folder}/topics`,
  `GET /api/groups/{folder}/messages` — chat UI JSON reads
  (`api.go`).
- `GET /x/*` — HTMX partials (`partials.go`).
- `GET|POST /chat/{token}/...` — public unauthenticated guest surface:
  chat widget root + config, message post, history, turn snapshot /
  status / SSE, and `POST /chat/{token}/mcp` (`route_token.go`,
  `chat_mcp.go`, `turn.go`). The URL token IS the capability.
- `GET|POST /slink/{token}/...` — legacy; 301 redirects to `/chat/...`.
- `GET|POST /me/...` — authed per-user chat console (`me.go`).
- `POST|GET|DELETE /mcp` — authed user MCP bridge (`mcp.go`); operator
  `routes.*` tools (`routes_mcp.go`).
- `GET /openapi.json` — engine-generated OpenAPI doc.
- `GET /static/*`, `GET /assets/*` — embedded assets.
- `GET /health`.

Planned per `specs/5/5-uniform-mcp-rest.md`:

- `/v1/web-routes` — REST verbs on owned tables.

## Token contract

- Authed surfaces (`/api/*`, `/x/*`, `/me/*`, `/mcp`, root `/`) use
  `requireUser` / `requireFolder` = `auth.RequireSignedOrBearer(hmacSecret, ks)`:
  accept either headers signed by `proxyd` (`PROXYD_HMAC_SECRET`) or a
  Bearer ES256 token verified against authd's JWKS (fetched via
  `auth.FetchKeys` at boot). webd never signs tokens.
- `/send`, `/typing`, `/v1/round_done` keep their existing
  `chanlib.Auth(channelSecret)` HMAC contract.
- `/chat/<token>/*` stays unauthenticated; the URL route-token IS the
  capability (looked up in `route_tokens`).

## Future work

webd's `/api/*` chat reads currently hit the shared DB directly. Once
routd ships `/v1/messages` per the platform API spec, migrate these
reads to routd.

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
- `route_token.go` — `route_tokens` lookup + `/chat/<token>` post/history/stream
- `chat_mcp.go` — chat-MCP transport at `POST /chat/<token>/mcp`
  (3 tools: send_message, get_round, get_round_status — mirroring
  the REST surface 1:1)
- `turn.go` — round-handle endpoints (`/chat/<token>/<id>{,/status,/sse}`)
- `me.go` — authed per-user chat console (`/me/*`)
- `mcp.go` — authed user MCP bridge at `/mcp`
- `routes_mcp.go` — operator `routes.*` MCP tools (thin adapter over proxyd `/v1/routes`)
- `api.go`, `pages.go`, `partials.go` — HTTP surface
- `assets.go` — embedded static asset serving

## Related docs

- `specs/5/5-uniform-mcp-rest.md` — federated `/v1/*` contract
- `specs/5/J-sse.md` — SSE streams + slink-MCP transport
- `specs/4/3-chat-ui.md`
- `ARCHITECTURE.md` (Web Channel section)
