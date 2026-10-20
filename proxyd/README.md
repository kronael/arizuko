# proxyd

proxyd is the one HTTP entry point of an instance. It authenticates the
caller, stamps the identity onto the request, and forwards the request to a
backend by path. Message routing is routd's and lives in
[ROUTING.md](../ROUTING.md); the trust rule for the stamped headers is in
[SECURITY.md § Identity header trust](../SECURITY.md#identity-header-trust).

## A request through proxyd

<!-- twin:proxyd -->

```
request
  │
  ▼
┌──────────────────┐   /auth/forward    forward-auth verdict
│ proxyd's own mux ├──►/auth/*          proxied to authd
│                  │   /v1/proxyd_routes, /openapi.json
│                  │   /dash/proxyd/    requireAuth, operator gate
└────────┬─────────┘
         │ every other path: strip the client's X-User-*,
         │ X-Chat-*, X-Folder; keep X-Forwarded-For only
         ▼ from a TRUSTED_PROXIES peer
┌──────────────────┐   /health          {"ok":true}
│ hard-wired paths ├──►/                mapped host? 302 /pub/W/
│                  │                    else 302 /pub/
│                  │   /pub/*           web_routes longest prefix:
│                  │                    redirect, deny, auth or vited
│                  │   /priv/*          requireAuth, MatchSlot, vited
└────────┬─────────┘
         ▼ one snapshot of proxyd_routes per request
┌──────────────────┐   /dav/*           requireAuth, davAllow, davd
│ MatchRoute       ├──►"/x/" prefix, "/x" exact, longest wins
│                  │   redirect_to      302
│                  │   auth=public      forward as sent
│                  │   auth=optional    stamp if a bearer verifies,
│                  │                    transit bearer either way
│                  │   auth=user        requireAuth, then forward
│                  │   auth=operator    as user; proxyd adds no check
│                  │                    of its own
└────────┬─────────┘
         ▼ no row matched
┌──────────────────┐   mapped host      302 /pub/W/<path>
│ final catch-all  ├──►any other host   302 /pub<path>
└──────────────────┘

requireAuth
  AUTH_SECRET unset ──► 404
  Bearer JWT, ES256 against authd's JWKS ──┐
  refresh_token cookie, authd /v1/refresh ─┤
                                           ▼
                  Authorization = service:proxyd bearer
                  X-User-Sub, X-User-Name
                  X-User-Groups = acl scopes, read per request
  neither ──► auth_return cookie, 303 /auth/login
```

<!-- /twin -->

The figure is the order in `server.route` (`proxyd/main.go`). Each stage
exists for one reason:

- **proxyd's own mux** answers the paths proxyd serves itself. These paths
  never reach `route`, so `/dash/proxyd/` carries its own `requireAuth`;
  without it the route table and its write forms would serve an anonymous
  caller. `/auth/forward` sits before the `/auth/*` proxy, so proxyd answers
  it and authd never sees it.
- **The header strip** runs once, before any decision. No client-supplied
  identity header survives ingress, so a backend that reads one cannot be
  fed it from outside.
- **The hard-wired paths** stay in Go because a row cannot express them.
  `/pub/*` probes `PUB_REDIRECT_URL` and passes websocket upgrades to
  vited. `/priv/*` checks the caller's scopes against the folder that owns
  the slot. `/` front-doors a mapped host.
- **One snapshot** of `proxyd_routes` serves the whole request. The matched
  route and its backend come from the same read, so a concurrent edit
  cannot pair one row's `auth` with another row's backend.
- **The final catch-all** turns any unmatched path into a public URL. Every
  reserved prefix was dispatched above it, so the host redirect here cannot
  shadow `/dash`, `/chat` or any other surface on any host.

### A world's host redirects to its public slot

proxyd maps `W.<HOSTING_DOMAIN>` to world `W` and answers `302
/pub/W/<path>`, with the path and the query kept. `WEB_VHOST_ALIASES` is
read first, for a host whose label is not the folder name. There is no
host-map file and no table: identity is configured, never derived from a
runtime path.

Four rules keep the mapping closed (`worldForHost`, `vhostRedirect`):

- The suffix check is on a label boundary, so `notkrons.fiu.wtf` never
  maps to `krons`.
- `W` is one label. A host with more labels, or the bare
  `HOSTING_DOMAIN`, maps to nothing.
- proxyd does no existence lookup. An unknown world also answers 302.
- `Location` is always a relative `/pub/...` path, so an attacker-supplied
  `Host` cannot open-redirect. A path with `..`, `%2e%2e` or `%2f` is 400.

The redirect runs at bare `/` and at the final catch-all, nowhere else. The
302 lands in `/pub/`, which never redirects again, so it cannot loop.
`worldForHost` and `vhostRedirect` stay in proxyd for three reasons: a row
keys on the path and these key on the `Host`, they run on every unmatched
request, and a world's front door must not depend on webd.

## Route rows

`proxyd_routes` is the only route configuration. proxyd reads it on every
request and never from env. An env var read once at boot stops applying
without a sign when a package changes the set; a row cannot. A new row is
live on the next request. There is no reload verb and no `enabled` column; to disable a
route, delete its row.

`arizuko generate` writes the release's rows. It takes the static
`coreProxydRoutes` slice in `compose/compose.go` and the `proxyd_routes`
block of every installed package's `services/<name>.yaml`, drops each route
whose `gated_by` variable is off, and writes the rest through
`store.SyncProxydRoutes`. The manifest is the same file `arizuko packages
install` applies: one declaration, two readers, because generate needs the
routes before any daemon or store exists.

| Field              | Meaning                                                                                                                                                            |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `path`             | With a trailing `/` it is a prefix; without one it is an exact match. The longest match wins. There is no glob and no regex (`MatchRoute`, `routes.go`).           |
| `backend`          | The URL proxyd forwards to. The outbound `Host` is the backend's, not the caller's.                                                                                |
| `auth`             | `public`, `optional`, `user` or `operator`; the next table.                                                                                                        |
| `gated_by`         | One env var name. `generate` drops the route when it is off; proxyd never reads the field, so every row in the table already passed its gate.                      |
| `preserve_headers` | Inbound headers proxyd sets again on the outbound request after the rewrite, such as Slack's signature pair. A backend that verifies its own signature names them. |
| `strip_prefix`     | Default `false`. When `true`, proxyd removes `path` from the forwarded URL; `/dav/` uses it.                                                                       |
| `redirect_to`      | When set, proxyd answers 302 to it and `backend` is empty.                                                                                                         |
| `owner`            | Who may rewrite the row; below. The writer stamps it, never the caller.                                                                                            |

| `auth`     | What proxyd does                                                                                                                                                      |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `public`   | Forwards the request as sent. The backend verifies the caller itself, for example Slack's HMAC on `/slack/`.                                                          |
| `optional` | Stamps the identity when a bearer verifies, forwards anonymously when none does, and attaches the `service:proxyd` bearer either way. `/chat/` and `/hook/` carry it. |
| `user`     | Runs `requireAuth`, then forwards.                                                                                                                                    |
| `operator` | The same as `user`. proxyd adds no check of its own.                                                                                                                  |

`optional` attaches the transit bearer on the anonymous path too, because
webd's `/chat/stream` admits only a request that came through proxyd
(`transited`). Without the bearer every anonymous visitor on an instance
with a JWKS gets 403. On the route-token surface the token is the
credential, and a stamped sub decides only who a message is attributed to.

Each row carries an `owner`:

- `arizuko` — written by `generate` from `coreProxydRoutes`. Every generate
  rewrites it, so a route added to a release reaches an existing instance.
- `package:<name>` — written by `generate` from that package's manifest,
  rewritten the same way. `generate` stamps the owner from the fragment's
  name and never trusts a value in the file.
- `operator` — created through `/v1/proxyd_routes` or the dashboard.
  `generate` never touches it, at any path.

`PATCH` on a row that `generate` owns is 409, because the edit would last
only until the next deploy. To override a shipped route, `DELETE` it and
`POST` your own; the path then belongs to the operator for good. A package
may not declare a path that `coreProxydRoutes` serves, and `generate`
refuses such a manifest. The one exception is a fragment the release
ships, which may serve its own `/dash/<name>/`.

## Changing routes at runtime

`/v1/proxyd_routes` is the operator surface. The same handler in
`proxyd/resource.go` serves five REST endpoints and the five MCP tools
`proxyd_routes.list`, `.get`, `.create`, `.update` and `.delete`, which
webd's `/mcp` bridge exposes to an operator's MCP client. The agent socket
does not carry them.

```
GET    /v1/proxyd_routes
GET    /v1/proxyd_routes/{path}       # path urlencoded, e.g. /v1/proxyd_routes/%2Fslack%2F
POST   /v1/proxyd_routes              # body: {path, backend, auth, gated_by?, preserve_headers?, strip_prefix?}
PATCH  /v1/proxyd_routes/{path}       # body: any subset of the create fields
DELETE /v1/proxyd_routes/{path}       # idempotent (204 either way)
```

The name is `proxyd_routes`, never `routes`: `routes` is routd's
message-routing table, and a resource's name is its wire identity.

**Forwarder allowlist.** proxyd trusts a stamped `X-User-*` on this surface
only behind an ES256 service bearer whose subject is in `trustedForwarders`
(`proxyd/resource.go`): `service:webd` and `service:dashd`. Any other valid
authd token that reaches proxyd directly gets 401, because a caller who
stamps its own groups could stamp itself an operator.

**Authorization.** The gate is `auth.Authorize(caller,
"proxyd_routes.<action>", "", nil)` at an empty scope, so only an operator
row such as `(google:op, '*', '**')` matches. The route table is global and
has no folder axis, so there is no narrower grant to hand out. No match is a
deny; there is no fallback.

**The dashboard.** proxyd serves `/dash/proxyd/` from its own mux: the table
in match order, longest prefix first, with an add form and a delete form.
The forms call the same store writers as the `/v1` handler, so a row has
one writer. A row created there is `operator`-owned.

## Forward auth (`GET /auth/forward`)

The subrequest surface for a proxy that fronts arizuko: Caddy
`forward_auth`, Traefik `ForwardAuth`, nginx `auth_request`. It answers 200
plus `auth.TransitHeaders`, which is the identity and the `service:proxyd`
bearer that makes a backend trust it. Otherwise it answers the same
303-to-login a direct request gets, with `auth_return` set to the caller's
route from `X-Forwarded-Uri`.

`decideForwardAuth` runs `requireAuth`'s attempts in `requireAuth`'s order,
so the endpoint cannot admit anyone the in-process gate refuses. One
difference: a refresh must set a cookie, and a subrequest has no upstream
response to carry it. An expired access token therefore takes the login
flow, which costs one more round trip and admits the same callers.

proxyd never caches the decision. A cached answer is a revocation that does
not take effect. The proof is the bearer proxyd already holds, which bounds
a captured answer to that bearer's remaining life
([SECURITY.md § Handing the proof to a fronting proxy](../SECURITY.md#handing-the-proof-to-a-fronting-proxy-get-authforward)).

`arizuko proxy-config <instance> --forward-auth <host:port>` renders the
rows as a Caddyfile pointed at this endpoint. The render rules are in
[`proxycfg/README.md`](../proxycfg/README.md).

## Why proxyd reads routd's tables directly

routd owns `acl`, and the standing rule is that another daemon reaches an
owner's rows over the owner's HTTP face. proxyd is the one named exception:
**it holds a reader on the owner's tables and reads them on the per-request
hot path.** `store.OpenOwner(store.OwnerRoutd, …)` opens the endpoint when
`DYNAMODB_URL` names one; proxyd declares none of the tables it reads.

proxyd reads three things per request: `proxyd_routes`, `web_routes` under
`/pub/*`, and `acl` through `auth.UserScopes` in `groupsForSub` for every
ES256-authenticated caller. Route-token resolution is webd's, so that row
has one reader. The alternative to the `acl` read is trusting the
login-time `Scope` snapshot in the JWT. A revoked grant would then keep
working until the caller's next token refresh, up to 15 minutes
(`accessTTL`). Reading `acl` per request makes revocation take effect on
the next request. `proxyd/hotpath_read_test.go` pins both halves.

The exception does not mean "a daemon holding a reader may read anything".
dashd holds one too, and its audit page reads over HTTP because it is not
on a per-request path.

## WebDAV write-block

Paths under `/dav/` reach `dufs` only after a `davAllow` check on top of the
group-scoped routing. compose mounts `groups/` into davd read-write and
starts `dufs` with `--allow-all`, so `davAllow` is the only write guard:

- Any path under `<group>/logs/` is read-only, at any folder depth. The
  read methods are `GET`, `HEAD`, `OPTIONS` and `PROPFIND`.
- Sensitive segments are write-blocked anywhere in the path: `.env`, any
  `*.pem`, any `.git`. A workspace clone, the agent's `.env` and TLS keys
  cannot be overwritten through WebDAV.

`DAV_PORT` publishes davd on the host's loopback. A request to that port
does not pass through proxyd, so no `davAllow` check runs on it.

`COPY` and `MOVE` carry a second write target in the `Destination` header,
and both checks run on it too. proxyd resolves the header the way `dufs`
does: a URI parse with the scheme and authority discarded, one
percent-decode, then Rust `Path::components` semantics. It rewrites the
header to the path it authorized before forwarding, so the backend resolves
what proxyd wrote rather than what the caller sent. A destination outside
`/dav/`, unparseable, or carrying a `..` is `400 Bad Request`.

Bare `/dav` redirects to the caller's first group in sorted order that is
not `**`, or to `root` when there is none. Blocked requests log `"dav blocked"`, or `"dav destination
blocked"` and `"dav destination forbidden"` for the header, and return
`403 Forbidden`.

## Entry points

Binary: `proxyd/main.go`, listen on `$PROXYD_LISTEN` (default `:8080`).

- `/health` — healthcheck
- `/auth/*` — proxied to authd, so the browser sees only the public host
- `/auth/forward` — the subrequest surface for a fronting proxy
- `/pub/*` — public files (vited or the `PUB_REDIRECT_URL` redirect)
- `/priv/*` — auth-gated private files (vited)
- `/dav/*` — WebDAV (auth-gated, group-scoped, write-blocked)
- `/v1/proxyd_routes` — operator REST and MCP resource
- `/dash/proxyd/` — proxyd's own operator dashboard, read and forms
- every `proxyd_routes` row, for example `/dash/`, `/chat/`, `/api/`

## Dependencies

- `auth` (ES256 JWT verify against authd's JWKs), `chanlib`, `core`, `store`, `resreg`

## Configuration

- `PROXYD_LISTEN` (default `:8080`)
- `VITE_ADDR` (default `http://vited:8080`) — backend for `/pub/*`, `/priv/*`
- `AUTH_SECRET` — keys the OAuth CSRF state; unset means `requireAuth` answers
  404, because the login flow cannot complete
- `AUTHD_URL` — authd endpoint for ES256 JWKs, `/v1/refresh` and the `/auth/*` proxy
- `AUTHD_SERVICE_KEY` — proxyd's service key for ES256 bearer minting
- `AUTHD_SERVICE_NAME` (default `proxyd`)
- `TRUSTED_PROXIES` — comma-separated CIDRs; bare IPs get `/32` or `/128`
- `HOSTING_DOMAIN` — derive world hosts from `<world>.<HOSTING_DOMAIN>`
- `WEB_VHOST_ALIASES` — host-to-world entries read before the derivation
- `PUB_REDIRECT_URL` — optional. When set and the probe succeeds, `/pub/*`
  returns `302` to `<url>/<rest>` with the path and query kept. Websocket
  upgrades and probe failures fall back to vited. The HEAD probe has a 2 s
  timeout; proxyd caches the result for 30 s.

## Health signal

`GET /health` returns `{"ok":true}` with status 200. A request with no valid
credential gets a 303 to `/auth/login` and an `auth_return` cookie; an
untrusted caller on `/v1/proxyd_routes` gets 401.

## Observability

The two HTTP metrics every daemon emits, at `GET /metrics` under
`METRICS_ENABLED=true`; proxyd adds none of its own (`obs/README.md`).

## Files

- `main.go` — config, path routing, auth gate, identity stamping, host derivation
- `routes.go` — `Route` and its matcher
- `resource.go` — `/v1/proxyd_routes` REST and MCP resource, store-backed route CRUD
- `dash.go` — `/dash/proxyd/`: the route table and its create and delete forms
- `forward_auth.go` — `decideForwardAuth`, the subrequest verdict

## Coverage

`go test ./proxyd/ -short -cover` reports 68.8% (2026-09-13). The gap is one
shape: boot and the mutating dashboard forms.

- `main`, `loadConfig`, `newServer`, `loadInitialRoutes`, `routes`,
  `newPubRedirect`, `defaultProbe` — the wiring that listens on a port and
  exits non-zero. Exercised by `make test-e2e`.
- `handleDashRouteCreate` / `handleDashRouteDelete` and their two helpers —
  the `/dash/proxyd/` forms. The same mutations ARE covered through
  `/v1/proxyd_routes` (`resource_test.go`), which is the handler they call;
  what is untested is the form parse in front of it.

Routing, the auth gate, identity stamping, vhost derivation, the WebDAV
write-block and forward auth are covered.

## Related docs

- [ARCHITECTURE.md § Web channel](../ARCHITECTURE.md#web-channel-proxyd)
- [SECURITY.md](../SECURITY.md) — the transit bearer and the trust zones
- [webd/README.md](../webd/README.md) — the route-token surface behind `/chat/` and `/hook/`
