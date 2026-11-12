# webd

webd is the web channel adapter and the per-user portal. It puts an agent in
a browser and streams each reply back. proxyd sits in front of it, see
[`proxyd/README.md`](../proxyd/README.md). routd routes what it posts, see
[ROUTING.md](../ROUTING.md).

## Purpose

webd registers with routd as channel `web` (prefix `web:`) and posts inbound
web messages through the standard `/v1/messages` API. It hosts the chat
widget behind a route token, the `/me` portal, and the MCP surface for
callers outside the guest.

webd and dashd split by audience. webd serves the people who talk to an
agent: the widget, `/me/*` and the `/mcp` bridge. dashd serves the operator.
`/me` lives in webd because it is the same chat surface for a signed-in
user; it needs no second daemon. Every `/me` read scopes by the caller's
stamped identity, `X-User-Sub` and `X-User-Groups` from proxyd, and never by
a sub in the URL (`webd/me.go`).

## Responsibilities

- Register as channel `web` with caps `send_text`, `edit` + `delete` (`webCaps` in
  `channel.go`). `typing` is not advertised: webd renders no presence;
  `POST /typing` still answers `ok`.
- Edit a frame by replacing it. `POST /edit` publishes a `message` frame
  with the id of the frame being edited and the new content on the hub key
  of the chat JID from the wire; a consumer that holds that id replaces the
  frame, one that does not appends it. The frame's `kind` is `status` when
  the row's verb is `status`, else `message` (`frameKind`, `turn.go`); the
  text never decides. An unknown target is 404, a row of another chat 404,
  a user message 403.
- Delete a frame by removing it. `POST /delete` publishes a `delete` frame
  `{id, turn_id}` on the hub key and topic of the original frame; the SDK
  hands it to `onDelete`. Routd ends a turn's progress line this way. A turn
  replay (`TurnFrames`) never returns a `status` row, so a reload does not
  bring the line back. Same 404 and 403 as `/edit`.
- Run the SSE hub that fans agent output to subscribers, keyed on
  the chat JID verbatim + topic (`hub.go`).
- Serve guest chat pages: public widget, token-scoped chat at
  `/chat/<token>/`, fire-and-forget webhooks at `/hook/<token>`
  (`route_token.go`, `pages.go`). At ingest webd stamps the sender —
  real `sub`/name when the caller carries a verified JWT, else
  `anon:<ip-hash>` — and snapshots the token's optional `context` onto
  the inbound as `link_context`.
- Serve ONE MCP surface (`mcp.go`) behind two doors: `/mcp` for an identity
  (N folders) and `/chat/<token>/mcp` for a route token (exactly 1).
- Accept proxyd-stamped identity headers verified via ES256 bearer
  (`auth.ProxydTransit`).

## Tables owned

None, and webd runs no migration. It holds ONE reader on routd's owner
(`store.OpenOwner(store.OwnerRoutd, …)` — the endpoint when `DYNAMODB_URL`
names one) for `route_tokens`, `groups`, `messages` and `turn_results`, and
writes `audit_log` through the same handle: one owner, one reader.

Inbound messages go through routd's `POST /v1/messages` like any channel
adapter. The `/api/*` chat reads still go through that reader rather than
routd's `/v1`.

`web_routes` is routd's, not webd's — routd serves it at `/v1/web_routes` and
renders its controls on routd's own page. Per-world hostnames are derived by
proxyd (`<world>.<HOSTING_DOMAIN>` → `/pub/<world>/`), not stored; webd has
no vhost config.

## Surface

Shipped today (see `server.go`):

- `POST /send`, `POST /edit`, `POST /delete`, `POST /typing`, `POST /v1/round_done` — channel adapter
  inbound from routd, verified via `chanlib.Auth` (ES256 `service:routd` token
  when `AUTHD_URL` set, open in local dev).
  `round_done` carries the CHAT JID verbatim, never the routing target and
  never a trimmed form: a subscriber that keys on the route's target folder
  never matches, and the browser waits forever (`routd/loop.go`
  `publishRoundDone`).
- `GET /api/groups`, `GET|POST /api/groups/{rest...}` — authed chat UI JSON
  reads/writes (groups list, topics, messages, typing); routed via
  `splitFolderSuffix` (`api.go`).
- `GET /x/groups`, `GET /x/groups/{rest...}` — authed HTMX partials
  (`partials.go`).
- `GET|POST /chat/{token}/...` — public unauthenticated guest surface:
  chat widget root + config, message post, history, turn snapshot/status/SSE,
  and `POST /chat/{token}/mcp` (`route_token.go`, `mcp.go`, `turn.go`).
  The URL token IS the capability.
- `POST /hook/{token}` — fire-and-forget webhook ingest (rate-limited).
- `GET|POST /pair/{token}` — identity pairing (`pair.go`).
  `GET` renders the confirm page and writes nothing — chat platforms
  unfurl links and an unfurl bot must not spend a pairing. `POST` writes
  the `acl_membership` edge and consumes the token in one routd
  transaction, behind a double-submit CSRF token.
- `GET /chat/stream` — SSE stream for route-token topics (`hub.go`).
- `GET|POST /slink/{token}/...` — answers 308 to `/chat/...`.
- `GET|POST /me/...` — authed per-user chat console: index, chats list,
  new chat, settings, folder/thread views, HTMX partials (`me.go`).
- `POST|GET|DELETE /mcp` — authed user MCP bridge (`mcp.go`): `list_groups`,
  `send`, `get_history`, plus the operator `proxyd_routes.*` tools
  (`routes_mcp.go`).
- `GET /{$}` — authed groups page root.
- `GET /panel/{folder...}` — authed folder-scoped chat page.
- `GET /dash/webd/` — webd's own operator dashboard. Read-only by
  design: the SSE hub's state lives only in webd's memory, which is why the
  page belongs to the owning daemon — a store snapshot cannot see a live
  subscriber.
- `GET /openapi.json` — engine-generated OpenAPI doc (public, pre-auth).
- `GET /static/*`, `GET /assets/*` — embedded static assets (permissive CORS on `/assets/*`).
- `GET /health`.

Turn and topic streams replay active progress independently of message cursors.
Turn streams emit `status` for the SDK's `onStatus` callback without advancing
the normal cursor. A `progress` event carries the authoritative `frames` array,
including an empty array. The built-in widget reconciles those nodes and removes
them on `delete` or `round_done`. History includes active progress and excludes
ended rows even when platform deletion failed.

## Token contract

- Authed surfaces (`/api/*`, `/x/*`, `/me/*`, `/mcp`, root `/`, `/panel/*`)
  use `requireUser` / `requireFolder` = `requireIdentified`: proxyd stamps
  `X-User-*` headers and proves the channel with its `service:proxyd` ES256
  bearer (verified via `auth.ProxydTransit` against authd's JWKS fetched at
  boot). The stamped header IS the identity, authenticated by the bearer.
  When `AUTHD_URL` unset (nil KeySet, local dev), the stamped header is
  trusted directly (no verifier, no proxyd). webd never signs tokens.
- `/send`, `/edit`, `/delete`, `/typing`, `/v1/round_done` are gated by `chanlib.Auth`:
  ES256 `service:routd` token when `AUTHD_URL` is set, open in local dev.
- `/pair/<token>` is the exception among token surfaces: proxyd gates it
  `user`, so an anonymous visitor is bounced through OAuth and returns to
  the same URL. The token names WHICH channel identity to link; the
  signed-in `sub` is WHO it links to, and the human's consent at the
  confirm step is the security boundary (`SECURITY.md` §"Pairing:
  consent is the boundary").
- `/chat/<token>/*` and `/hook/<token>` stay unauthenticated; the URL
  route-token IS the capability. webd resolves it (`lookupRouteToken` →
  `route_tokens`), and is the ONLY resolver: proxyd forwards the token and
  reads no row, so one row has one reader. proxyd's route carries
  `auth: optional`, so a caller who offers a bearer arrives identified and
  gets the message attributed to them; everyone else arrives anonymous.
- `GET /chat/stream` asks for one thing more: `transited(r)`, proxyd's
  `service:proxyd` bearer. A subscription is long-lived, so it must have come
  through the front door — but holding the token is the only credential.
- The anon DoS shield (`CHAT_ANON_DOS_RPM`) lives here too, keyed on the
  `X-Forwarded-For` client proxyd resolved. NEVER key it on `RemoteAddr`: webd's
  peer is always proxyd, so that puts every visitor in one bucket (BUGS L40).
- That lookup is **in-process and deliberately not an HTTP call to
  routd**: webd already holds a reader on the owner, and a per-request hop
  would only add a failure mode. The hop would buy no containment, because
  routd would read the same row for webd.

## Live frames: the SSE hub

The hub (`hub.go`) fans agent output to browsers. Five rules shape it:

- **The group is the boundary.** A subscriber sees every frame of its chat
  JID and topic. There is no per-sender filter inside a group, because a
  group is one shared context and a filter would split it.
- **The key is the chat JID verbatim, plus the topic.** The publisher and the
  subscriber each pass the JID they already hold: the token row, the turn,
  or `chat_jid` on the wire. Neither end derives a folder from it. Both ends use the full JID, including suffix and channel prefix.
- **`round_done` carries the chat JID, never the routing target.** With
  `web:X/submissions` routed to `groupY`, a subscriber keyed on `groupY`
  never matches (`channel.go`, routd's `publishRoundDone`).
- **A slow client loses frames; nothing waits for it.** Each subscriber has
  a 16-frame buffer, and `publish` drops a frame when the buffer is full.
  The hub holds at most 10,000 keys (`maxHubKeys`) and 256 subscribers per
  key (`maxSubsPerKey`). A public URL is a flood surface, so backpressure
  would let one reader stall every other.
- **Blocking poll is the other transport.** A JSON
  `POST /chat/<token>/?wait=<sec>` holds the response until the first
  assistant frame, up to 120 s, and
  `/chat/<token>/<turn_id>/status` answers without a stream. A proxy that
  breaks SSE therefore needs no second delivery path (`route_token.go`).

## The browser SDK

webd serves `/assets/arizuko-client.js` from an `embed.FS` in its binary
(`assets.go`), so the SDK version always matches the webd that serves it.
Only embedded files are served, so a path traversal has nothing to reach.
Each answer carries `Cache-Control: public, max-age=3600`, a content-hash
`ETag` and `Access-Control-Allow-Origin: *`; a widget on a third-party page
loads it with no session. `GET /chat/<token>/config` names the SDK path in
its `"sdk"` field, so a page discovers it from the token alone.

## What it does not do

- **No signing.** webd mints no token and verifies no password; the stamped
  `X-User-*` header IS the identity, and proxyd's `service:proxyd` bearer is
  what makes it trustworthy.
- **No route table.** The `proxyd_routes.*` MCP tools here are a thin identity bridge
  over proxyd's `/v1/proxyd_routes`; `Resource.Store` is nil, so proxyd runs
  the authorization and writes the audit row.
- **No shared SSE.** The hub is an in-process map, and routd publishes by
  POSTing to webd, so two webd processes do not see each other's subscribers.

## Entry points

- Binary: `webd/main.go`
- Listen: `WEBD_LISTEN` (default `:8080`)
- Routes wired in `server.go`
- Registers with routd as channel `web`, prefix `web:`, caps `send_text`, `edit`, and `delete`

## Dependencies

- `chanlib` (router client, env helpers), `core`, `store`

## Configuration

| Variable                                   | Default              | Meaning                                                                                                           |
| ------------------------------------------ | -------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `WEBD_LISTEN`                              | `:8080`              | HTTP listener; LISTEN_ADDR is not read.                                                                           |
| `WEBD_URL`                                 | `http://webd:8080`   | Callback URL advertised to routd.                                                                                 |
| `ROUTER_URL`                               | `http://routd:8080`  | Ingress and registration endpoint.                                                                                |
| `PROXYD_URL`                               | `http://proxyd:8080` | Operator route management forwarding.                                                                             |
| `ASSISTANT_NAME`                           | `assistant`          | Web display name.                                                                                                 |
| `AUTHD_URL`                                | unset                | JWKS source for service and transit verification.                                                                 |
| `AUTHD_SERVICE_KEY`                        | unset                | Bootstrap secret for service:webd.                                                                                |
| `AUTHD_SERVICE_NAME`                       | `webd`               | Service-token exchange principal.                                                                                 |
| `WEBHOOK_RATE_HOOK`                        | `60`                 | Requests/minute per hook token.                                                                                   |
| `WEBHOOK_RATE_WEB`                         | `20`                 | Requests/minute per chat token.                                                                                   |
| `CHAT_ANON_DOS_RPM`                        | `10`                 | Requests/minute per anonymous client IP; non-positive disables.                                                   |
| `DATA_DIR`                                 | working directory    | Shared instance config root; web assets are embedded, not served from this path.                                  |
| `DYNAMODB_URL`                             | required             | Store endpoint.                                                                                                   |
| `AWS_REGION`                               | `us-east-1`          | Store signing region.                                                                                             |
| `AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY` | required             | Daemon store identity.                                                                                            |
| `STORE_CA`                                 | unset                | Base64 PEM certificate; unset uses system trust.                                                                  |
| `ARIZUKO_INSTANCE`                         | unset                | Audit and observability instance label.                                                                           |
| `LOG_LEVEL`                                | `info`               | JSON stderr verbosity, including turn logs.                                                                       |
| `OTEL_EXPORTER_OTLP_ENDPOINT`              | unset                | Optional OTLP logs.                                                                                               |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`       | unset                | Optional OTLP trace export; independent of the log endpoint.                                                      |
| `MAX_CONCURRENT_CONTAINERS`                | absent               | Shared config rejects this variable whenever it is present; configure MAX_CONCURRENT_AGENTS for execution limits. |
| `METRICS_ENABLED`                          | `false`              | Literal true mounts public GET /metrics.                                                                          |

The shared loader parses [core/config.go](../core/config.go); webd selects the fields above.

## Health signal

`GET /health` returns 200 unconditionally once HTTP starts. It does not recheck registration, the store, or routd.
`store.LatestSource(jid)` identifies which adapter accepted a stored message; it is not a process health probe.

## Files

- `main.go`, `server.go` — wiring + routes + auth middleware
- `hub.go` — SSE fan-out keyed on chat JID + topic
- `channel.go` — routd→webd callbacks (`/send`, `/edit`, `/delete`, `/typing`, `/v1/round_done`)
- `route_token.go` — route-token lookup + `/chat/<token>` + `/hook/<token>` handlers
- `turn.go` — round-handle endpoints (`/chat/<token>/<id>{,/status,/sse}`)
- `me.go` — authed per-user chat console (`/me/*`)
- `mcp.go` — the one web MCP surface (`list_groups`, `send`, `get_history`)
  behind both doors: `/mcp` for an identity, `/chat/<token>/mcp` for a route
  token pinned to one group
- `routes_mcp.go` — the five operator `proxyd_routes.*` MCP tools, proxying proxyd's
  `/v1/proxyd_routes`
- `dash.go` — `/dash/webd/`, the read-only hub + registration overview
- `api.go` — authed chat UI JSON API (`/api/groups/*`)
- `pages.go` — authed HTML pages (root, panel)
- `partials.go` — authed HTMX partials (`/x/groups/*`)
- `assets.go` — embedded static asset serving (`/static/*`, `/assets/*`)
- `pair.go` — identity-pairing confirm + redemption (`/pair/<token>`)
- `ratelimit.go` — per-token rate limiter for `/chat/*` and `/hook/*`
- `anonshield.go` — per-client-IP anon DoS shield + `clientIP`

## Related docs

- [`ARCHITECTURE.md` § Message Flow](../ARCHITECTURE.md#message-flow)
- [`SECURITY.md` § Pairing](../SECURITY.md#pairing-consent-is-the-boundary) — the pairing surface webd serves
- [`resreg/README.md`](../resreg/README.md) — the federated `/v1/*` contract

## State, timing, and failures

`StoreReach["webd"]` reaches only `routd`; webd owns no store schema.
Reads use `groups`, `messages` and message indexes, `turn_context`, `turn_results`, `route_tokens`, grants/membership, and chat/session state.
Pairing consumes a token and writes membership through the shared store operations.
Audit events land in `routd.audit_log`. The SSE hub, rate buckets, and anonymous shield are process-local.
Static UI and SDK files are embedded. webd owns no persistent on-disk spool or socket.

Registration failure or a store-open error exits. Registration repeats every 3 minutes through chanlib.
Router requests have a 60 s timeout and inbound posting tries twice with a 500 ms wait.
There is no adapter-local inbound retention store. A rejected web submission returns an error to its HTTP caller.
SSE sends keepalive comments every 15 s with a 10 s write deadline.
The turn-specific stream also checks status every 15 s. Blocking wait is capped at 120 s.
The proxyd route-management client times out after 10 s. Shutdown closes HTTP immediately.
A routd outage stops submissions; a running hub can still accept channel callbacks until requests fail.
A store outage prevents token lookup and replay. A slow subscriber can lose live frames and must read history.
Grep `open routd's store`, `router registration failed`, `channel heartbeat failed`,
`webd: identity verify failed`, and `folder access denied`.
`/metrics` reports HTTP request counts/duration when enabled. Logs and traces use obs.

Known limits: live/replayed frame IDs ([BUGS L440](../BUGS.md)), initial status topic placement ([BUGS L439](../BUGS.md)),
and anonymous client identity behind an unconfigured edge ([BUGS L40](../BUGS.md)).
The web MCP door offers `list_groups`, `send`, and `get_history`; route-token MCP confines these to its bound chat.
Operator `proxyd_routes.*` tools forward to proxyd; no cold-tier resource is owned here.
Tests cover handlers and frame behavior with stubs; local documentation checks do not verify a live browser or edge SSE buffering.

Sources: [boot and env](main.go), [mux and auth](server.go), [hub](hub.go), [turn streams](turn.go),
[MCP route forwarding](routes_mcp.go), [store reach](../store/owner_reach.go).

## Mounted route patterns

The table transcribes the mux patterns; the auth rules above apply to each surface.

| Method and path                      | Owner |
| ------------------------------------ | ----- |
| `GET /static/`                       | webd  |
| `GET /assets/{path...}`              | webd  |
| `OPTIONS /assets/{path...}`          | webd  |
| `POST /send`                         | webd  |
| `POST /edit`                         | webd  |
| `POST /delete`                       | webd  |
| `POST /typing`                       | webd  |
| `POST /v1/round_done`                | webd  |
| `GET /health`                        | webd  |
| `GET /openapi.json`                  | webd  |
| `GET /chat/{token}/{$}`              | webd  |
| `GET /chat/{token}/config`           | webd  |
| `POST /chat/{token}`                 | webd  |
| `POST /chat/{token}/`                | webd  |
| `POST /chat/{token}/mcp`             | webd  |
| `GET /chat/{token}/{topic}/messages` | webd  |
| `GET /chat/stream`                   | webd  |
| `GET /chat/{token}/{id}`             | webd  |
| `GET /chat/{token}/{id}/status`      | webd  |
| `GET /chat/{token}/{id}/sse`         | webd  |
| `POST /hook/{token}`                 | webd  |
| `OPTIONS /chat/`                     | webd  |
| `OPTIONS /hook/`                     | webd  |
| `GET /slink/{token}/{rest...}`       | webd  |
| `POST /slink/{token}/{rest...}`      | webd  |
| `GET /slink/{token}`                 | webd  |
| `POST /slink/{token}`                | webd  |
| `OPTIONS /slink/`                    | webd  |
| `GET /dash/webd/`                    | webd  |
| `POST /mcp`                          | webd  |
| `GET /mcp`                           | webd  |
| `DELETE /mcp`                        | webd  |
| `GET /me/`                           | webd  |
| `GET /me/chats`                      | webd  |
| `GET /me/chats/new`                  | webd  |
| `POST /me/chats/new`                 | webd  |
| `GET /me/settings`                   | webd  |
| `PATCH /me/settings`                 | webd  |
| `GET /me/x/folders`                  | webd  |
| `GET /me/x/chats`                    | webd  |
| `GET /me/x/thread`                   | webd  |
| `GET /me/chats/{folder...}`          | webd  |
| `POST /me/chats/{folder...}`         | webd  |
| `GET /me/folders/{folder...}`        | webd  |
| `GET /pair/{token}`                  | webd  |
| `POST /pair/{token}`                 | webd  |
| `GET /{$}`                           | webd  |
| `GET /panel/{folder...}`             | webd  |
| `GET /api/groups`                    | webd  |
| `GET /api/groups/{rest...}`          | webd  |
| `POST /api/groups/{rest...}`         | webd  |
| `GET /x/groups`                      | webd  |
| `GET /x/groups/{rest...}`            | webd  |
| `GET /metrics`                       | webd  |

JWKS keys load lazily during verification through auth.FetchKeys; there is no fixed background JWKS ticker.
Service tokens refresh on demand within 60 s of expiry, with a 10 s exchange timeout ([auth/service.go](../auth/service.go)).
