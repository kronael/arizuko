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
  `/chat/<token>/`, fire-and-forget webhooks at `/hook/<token>`
  (`route_token.go`, `pages.go`).
- Serve the user MCP bridge for authed sessions (`mcp.go`) and the
  token-only chat-MCP transport at `/chat/<token>/mcp` (`chat_mcp.go`).
- Accept proxyd-stamped identity headers verified via ES256 bearer
  (`auth.ProxydTransit`).

## Tables owned

None. webd reads `route_tokens`, `groups`, `messages`, `turn_results` from
routd.db (owned by routd) and `audit_log` from messages.db. It writes messages
as a channel adapter via routd's `POST /v1/messages` (inbound path) and today
reads them via its own `/api/*` paths directly off routd.db. Once federation
lands, those reads migrate to routd's `GET /v1/messages`. Per-world hostnames
are derived by proxyd (`<world>.<HOSTING_DOMAIN>` → `/pub/<world>/`), not
stored (`specs/5/V-web-vhosts.md`); webd has no vhost config.

Planned per `specs/5/5-uniform-mcp-rest.md`: `web_routes` table for owned
web-specific routing config (future).

## Surface

Shipped today (see `server.go`):

- `POST /send`, `POST /typing`, `POST /v1/round_done` — channel adapter
  inbound from routd, verified via `chanlib.Auth` (ES256 `service:routd` token
  when `AUTHD_URL` set, open in local dev).
- `GET /api/groups`, `GET|POST /api/groups/{rest...}` — authed chat UI JSON
  reads/writes (groups list, topics, messages, typing); routed via
  `splitFolderSuffix` (`api.go`).
- `GET /x/groups`, `GET /x/groups/{rest...}` — authed HTMX partials
  (`partials.go`).
- `GET|POST /chat/{token}/...` — public unauthenticated guest surface:
  chat widget root + config, message post, history, turn snapshot/status/SSE,
  and `POST /chat/{token}/mcp` (`route_token.go`, `chat_mcp.go`, `turn.go`).
  The URL token IS the capability.
- `POST /hook/{token}` — fire-and-forget webhook ingest (rate-limited).
- `GET /chat/stream` — SSE stream for route-token topics (`hub.go`).
- `GET|POST /slink/{token}/...` — legacy; 301 redirects to `/chat/...`.
- `GET|POST /me/...` — authed per-user chat console: index, chats list,
  new chat, settings, folder/thread views, HTMX partials (`me.go`).
- `POST|GET|DELETE /mcp` — authed user MCP bridge (`mcp.go`); includes
  operator `routes.*` tools (`routes_mcp.go`).
- `GET /{$}` — authed groups page root.
- `GET /panel/{folder...}` — authed folder-scoped chat page.
- `GET /openapi.json` — engine-generated OpenAPI doc (public, pre-auth).
- `GET /static/*`, `GET /assets/*` — embedded static assets (permissive CORS on `/assets/*`).
- `GET /health`.

Planned per `specs/5/5-uniform-mcp-rest.md`:

- `/v1/web-routes` — REST verbs on owned tables.

## Token contract

- Authed surfaces (`/api/*`, `/x/*`, `/me/*`, `/mcp`, root `/`, `/panel/*`)
  use `requireUser` / `requireFolder` = `requireIdentified`: proxyd stamps
  `X-User-*` headers and proves the channel with its `service:proxyd` ES256
  bearer (verified via `auth.ProxydTransit` against authd's JWKS fetched at
  boot). The stamped header IS the identity, authenticated by the bearer.
  When `AUTHD_URL` unset (nil KeySet, local dev), the stamped header is
  trusted directly (no verifier, no proxyd). webd never signs tokens.
- `/send`, `/typing`, `/v1/round_done` are gated by `chanlib.Auth`:
  ES256 `service:routd` token when `AUTHD_URL` is set, open in local dev.
- `/chat/<token>/*` and `/hook/<token>` stay unauthenticated; the URL
  route-token IS the capability (looked up in `route_tokens`, resolved to
  folder via proxyd's `X-Chat-Token` + `X-Folder` stamps verified via
  `chatTransit`).

## Future work

webd's `/api/*` chat reads currently hit the shared DB directly. Once
routd ships `/v1/messages` per the platform API spec, migrate these
reads to routd.

## Entry points

- Binary: `webd/main.go`
- Listen: `WEBD_LISTEN` (default `:8080`)
- Routes wired in `server.go`
- Registers with routd as channel `web`, prefix `web:`, caps `send_text` + `typing`

## Dependencies

- `chanlib` (router client, env helpers), `core`, `store`

## Configuration

- `WEBD_LISTEN` (default `:8080`), `WEBD_URL` (default `http://webd:8080`)
- `ROUTER_URL` (default `http://routd:8080`), `PROXYD_URL` (default `http://proxyd:8080`)
- `ASSISTANT_NAME` (default `assistant`)
- `AUTHD_URL` — when set, enables ES256 bearer verification via JWKS; unset → HMAC-only (local dev)
- `AUTHD_SERVICE_KEY`, `AUTHD_SERVICE_NAME` (default `webd`) — service token for routd + proxyd calls
- `WEBHOOK_RATE_HOOK` (default 60), `WEBHOOK_RATE_WEB` (default 20) — per-token req/min rate limits
- `ARIZUKO_INSTANCE` — instance name for audit/observability

## Health signal

`GET /health` returns 200 when registered with the router. Liveness also
observable via `store.LatestSource(jid)` returning `web` for recently
delivered messages.

## Files

- `main.go`, `server.go` — wiring + routes + auth middleware
- `hub.go` — SSE fan-out keyed on `folder/topic`
- `channel.go` — routd→webd callbacks (`/send`, `/typing`, `/v1/round_done`)
- `route_token.go` — route-token lookup + `/chat/<token>` + `/hook/<token>` handlers
- `chat_mcp.go` — chat-MCP transport at `POST /chat/<token>/mcp`
  (3 tools: `send_message`, `get_round`, `get_round_status`)
- `turn.go` — round-handle endpoints (`/chat/<token>/<id>{,/status,/sse}`)
- `me.go` — authed per-user chat console (`/me/*`)
- `mcp.go` — authed user MCP bridge at `/mcp`
- `routes_mcp.go` — operator `routes.*` MCP tools (proxies proxyd `/v1/routes`)
- `api.go` — authed chat UI JSON API (`/api/groups/*`)
- `pages.go` — authed HTML pages (root, panel)
- `partials.go` — authed HTMX partials (`/x/groups/*`)
- `assets.go` — embedded static asset serving (`/static/*`, `/assets/*`)
- `ratelimit.go` — per-token rate limiter for `/chat/*` and `/hook/*`

## Related docs

- `specs/5/5-uniform-mcp-rest.md` — federated `/v1/*` contract
- `specs/5/J-sse.md` — SSE streams + slink-MCP transport
- `specs/4/3-chat-ui.md`
- `ARCHITECTURE.md` (Web Channel section)
