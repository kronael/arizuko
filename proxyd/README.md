# proxyd

proxyd is the one HTTP entry point of an instance. It authenticates the caller, stamps the identity onto the request and forwards the request to a backend by path. routd routes messages, and `ROUTING.md` describes that. The trust rule for the stamped headers is in [SECURITY.md](../SECURITY.md#only-proxyd-stamps-identity-headers).

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

The figure shows the order in `server.route` (`proxyd/main.go`). Each stage exists for one reason.

- **proxyd's own mux** answers the paths that proxyd serves itself. These paths never reach `route`, so `/dash/proxyd/` carries its own `requireAuth`. Without it, the route table and its write forms would serve an anonymous caller. `/auth/forward` sits before the `/auth/*` proxy, so proxyd answers it and authd never sees it. An `/auth/*` path that no mux entry serves falls through to `route`. `route` treats it as a private surface and gates it with `requireAuth`, so it bounces to `/auth/login` and not to the public `/pub/` redirect.
- **The header strip** runs once, before any decision. No identity header that a client supplies survives ingress, so nobody outside can feed one to a backend that reads it.
- **The hard-wired paths** stay in Go because a row cannot express them. `/pub/*` probes `PUB_REDIRECT_URL` and passes websocket upgrades to vited. `/priv/*` checks the scopes of the caller against the folder that owns the slot. `/` front-doors a mapped host.
- **One snapshot** of `proxyd_routes` serves the whole request. The matched route and its backend come from the same read, so a concurrent edit cannot pair the `auth` of one row with the backend of another row.
- **The final catch-all** turns any unmatched path into a public URL. Every reserved prefix is dispatched above it, so the host redirect here cannot shadow `/dash`, `/chat` or any other surface on any host.

### A world's host redirects to its public slot

proxyd maps `W.<HOSTING_DOMAIN>` to world `W` and answers `302 /pub/W/<path>`. It keeps the path and the query. It reads `WEB_VHOST_ALIASES` first, for a host whose label is not the folder name. There is no host-map file and no table, because identity is configured and never derived from a runtime path.

Four rules keep the mapping closed (`worldForHost`, `vhostRedirect`).

- The suffix check is on a label boundary, so `notkrons.fiu.wtf` never maps to `krons`.
- `W` is one label. A host with more labels, or the bare `HOSTING_DOMAIN`, maps to nothing.
- proxyd does no existence lookup. An unknown world also answers 302.
- `Location` is always a relative `/pub/...` path, so a `Host` that an attacker supplies cannot open-redirect. A path with `..`, `%2e%2e` or `%2f` gets 400.

The redirect runs at bare `/` and at the final catch-all, and nowhere else. The 302 lands in `/pub/`, which never redirects again, so it cannot loop. `worldForHost` and `vhostRedirect` stay in proxyd for three reasons. A row keys on the path, and these functions key on the `Host`. They run on every unmatched request. The front door of a world must not depend on webd.

### web_routes hold the rules of a group for its public tree

`web_routes` apply only under `/pub/*`. A request there passes three layers, from the outer to the inner layer.

1. The host redirect of proxyd maps `W.<HOSTING_DOMAIN>` to `/pub/W/`.
2. proxyd matches the longest `web_routes` prefix of the path.
3. vited serves the file from `<data>/web/pub/`.

The path prefix is the coordinate of the row. The attributes are:

| Attribute     | Values                                                      |
| ------------- | ----------------------------------------------------------- |
| `access`      | `public`, `auth`, `deny` or `redirect`                      |
| `redirect_to` | used when `access` is `redirect`                            |
| `folder`      | the owning group, which is the caller and never an argument |

`public` and no match forward to vited. `auth` runs `requireAuth` of proxyd first. `deny` answers 403. `redirect` answers 302 with the matched prefix replaced by `redirect_to`.

Agents add and remove rows with the MCP tools `set_web_route`, `del_web_route` and `list_web_routes` (`routd/web_routes_resource.go`). routd makes every write. proxyd reads the whole inventory on every request (`store.AllWebRoutes`) and matches it in Go (`matchWebRoute`).

One URL has one backing store. The slot of a group is `/pub/<folder>/`, and these rules keep every other path from becoming a second copy of it.

- A group may always write a rule inside its own slot. A prefix outside the slot is first-claim. When another folder already holds a rule there, `set_web_route` answers 403 and names the owner.
- An alias is a `redirect` row and never a copy of the bytes. `redirect_to` must point into the `/pub/<folder>/` or `/priv/<folder>/` of the caller, so a row cannot open-redirect or speak for another group.
- A `redirect` row may not cover the `/pub/<folder>/` of another group. proxyd takes the longest prefix, so such a row would answer for the whole publish path of that group. `set_web_route` answers 409 and names the folders that it would take.
- Deleting a group deletes its rules. `DeleteGroup` sweeps `web_routes` by `folder` with the rest of the subtree (`store/folder_scope.go`), so a claim does not outlive its owner.

## Route rows

`proxyd_routes` is the only route configuration. proxyd reads it on every request and never from env. An env var that proxyd reads once at boot stops applying without a sign when a package changes the set, and a row cannot do that. A new row is live on the next request. There is no reload verb and no `enabled` column. To disable a route, delete its row.

`arizuko generate` writes the rows of the release. It takes the static `coreProxydRoutes` slice in `compose/compose.go` and the `proxyd_routes` block of each service's package manifest. For a symlinked service fragment, it reads `template/services/<name>/arizuko.yml` from the visible checkout or the baked catalog. Otherwise, it reads `packages/<name>/arizuko.yml` in the instance. It skips writes for routes whose `gated_by` variable is off, but does not delete existing rows. `arizuko package sync` applies declared package routes without evaluating `gated_by`. Both commands read the package manifest.

| Field              | Meaning                                                                                                                                                                      |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `path`             | With a trailing `/` it is a prefix. Without one it is an exact match. The longest match wins. There is no glob and no regex (`MatchRoute` in `routes.go`).                   |
| `backend`          | The URL that proxyd forwards to. The outbound `Host` is the host of the backend and not the host of the caller.                                                              |
| `auth`             | `public`, `optional`, `user` or `operator`, as the next table shows.                                                                                                         |
| `gated_by`         | One env var name. `generate` skips the write when the variable is off. `sync` and proxyd do not evaluate it.                                                                 |
| `preserve_headers` | Inbound headers that proxyd sets again on the outbound request after the rewrite, such as the signature pair of Slack. A backend that verifies its own signature names them. |
| `strip_prefix`     | The default is `false`. When it is `true`, proxyd removes `path` from the forwarded URL. `/dav/` uses it.                                                                    |
| `redirect_to`      | When set, proxyd answers 302 to it and `backend` is empty.                                                                                                                   |
| `owner`            | Who may rewrite the row, as described below. The writer stamps it and the caller never does.                                                                                 |

| `auth`     | What proxyd does                                                                                                                                                       |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `public`   | Forwards the request as sent. The backend verifies the caller itself, for example the HMAC of Slack on `/slack/`.                                                      |
| `optional` | Stamps the identity when a bearer verifies, forwards anonymously when none does, and attaches the `service:proxyd` bearer in both cases. `/chat/` and `/hook/` use it. |
| `user`     | Runs `requireAuth`, then forwards.                                                                                                                                     |
| `operator` | Does the same as `user`. proxyd adds no check of its own, and the backend daemon checks the grant of the operator.                                                     |

`optional` attaches the transit bearer on the anonymous path too, because the `/chat/stream` of webd admits only a request that came through proxyd (`transited`). Without the bearer, every anonymous visitor on an instance with a JWKS gets 403. On the route-token surface the token is the credential, and a stamped sub decides only who a message is attributed to.

Each row carries an `owner`.

- `arizuko`: `generate` writes the row from `coreProxydRoutes`. Every generate rewrites it, so a route that a release adds reaches an existing instance.
- `package:<name>`: `generate` writes the row from the manifest of that package and rewrites it the same way. `generate` stamps the owner from the name of the fragment and never trusts a value in the file.
- `operator`: the row was created through `/v1/proxyd_routes` or the dashboard. `generate` never touches it, at any path.

`PATCH` on a row that `generate` owns answers 409, because the edit would last only until the next deploy. To override a shipped route, `DELETE` it and `POST` your own. The path then belongs to the operator for good. A package may not declare a path that `coreProxydRoutes` serves, and `generate` refuses such a manifest. The one exception is a fragment that the release ships, which may serve its own `/dash/<name>/`.

## Changing routes at runtime

`/v1/proxyd_routes` is the operator surface. The same handler in `proxyd/resource.go` serves five REST endpoints and the five MCP tools `proxyd_routes.list`, `proxyd_routes.get`, `proxyd_routes.create`, `proxyd_routes.update` and `proxyd_routes.delete`. The `/mcp` bridge of webd exposes the tools to the MCP client of an operator. The agent socket does not carry them.

```
GET    /v1/proxyd_routes
GET    /v1/proxyd_routes/{path}       # path urlencoded, e.g. /v1/proxyd_routes/%2Fslack%2F
POST   /v1/proxyd_routes              # body: {path, backend, auth, gated_by?, preserve_headers?, strip_prefix?}
PATCH  /v1/proxyd_routes/{path}       # body: any subset of the create fields
DELETE /v1/proxyd_routes/{path}       # idempotent (204 either way)
```

The name is `proxyd_routes` and never `routes`. `routes` is the message-routing table of routd, and the name of a resource is its wire identity.

**Forwarder allowlist.** On this surface, proxyd trusts a stamped `X-User-*` only behind an ES256 service bearer whose subject is in `trustedForwarders` (`proxyd/resource.go`): `service:webd` and `service:dashd`. Any other valid authd token that reaches proxyd directly gets 401, because a caller that stamps its own groups could stamp itself an operator.

**Authorization.** The gate is `auth.Authorize(caller, "proxyd_routes.<action>", "", nil)` at an empty scope, so only an operator row such as `(google:op, '*', '**')` matches. The route table is global and has no folder axis, so no narrower grant exists to hand out. No match is a deny, and there is no fallback.

**The dashboard.** proxyd serves `/dash/proxyd/` from its own mux. It shows the table in match order, with the longest prefix first, and an add form and a delete form. The forms call the same store writers as the `/v1` handler, so a row has one writer. A row that a form creates has the owner `operator`.

## Forward auth, GET /auth/forward

This is the subrequest surface for a proxy that fronts arizuko: Caddy `forward_auth`, Traefik `ForwardAuth` or nginx `auth_request`. It answers 200 plus `auth.TransitHeaders`, which are the identity and the `service:proxyd` bearer that makes a backend trust it. Otherwise it answers the same 303 to login that a direct request gets, with `auth_return` set to the route of the caller from `X-Forwarded-Uri`.

`decideForwardAuth` runs the attempts of `requireAuth` in the order of `requireAuth`, so the endpoint cannot admit anyone that the in-process gate refuses. One difference exists. A refresh must set a cookie, and a subrequest has no upstream response to carry it. So an expired access token takes the login flow. That costs one more round trip and admits the same callers.

proxyd never caches the decision, because a cached answer is a revocation that does not take effect. The proof is the bearer that proxyd already holds, which limits a captured answer to the remaining life of that bearer. See [SECURITY.md](../SECURITY.md#a-fronting-proxy-receives-the-proof-for-up-to-15-minutes).

`arizuko proxy-config -i <instance> --forward-auth <host:port>` renders the rows as a Caddyfile that points at this endpoint. The render rules are in [`proxycfg/README.md`](../proxycfg/README.md).

## Why proxyd reads routd's tables directly

routd owns `acl`, and the standing rule is that another daemon reaches the rows of an owner over the HTTP face of the owner. proxyd is the one named exception. **It holds a reader on the tables of the owner and reads them on the per-request hot path.** `store.OpenOwner(store.OwnerRoutd, …)` opens the endpoint that `DYNAMODB_URL` names. proxyd declares none of the tables that it reads.

proxyd reads three things per request: `proxyd_routes`, `web_routes` under `/pub/*`, and `acl` through `auth.UserScopes` in `groupsForSub` for every ES256-authenticated caller. webd resolves route tokens, so that row has one reader. The alternative to the `acl` read is to trust the `Scope` snapshot in the JWT from login time. A revoked grant would then keep working until the next token refresh of the caller, up to 15 minutes (`accessTTL` in `authd/main.go`). Reading `acl` per request makes a revocation take effect on the next request. `proxyd/hotpath_read_test.go` pins both halves.

The exception does not mean that a daemon with a reader may read anything. dashd holds a reader too, and its audit page reads over HTTP, because that page is not on a per-request path.

## WebDAV write-block

Paths under `/dav/` reach `dufs` only after a `davAllow` check on top of the group-scoped routing. compose mounts `groups/` into davd read-write and starts `dufs` with `--allow-all`, so `davAllow` is the only write guard.

- Any path under `<group>/logs/` is read-only, at any folder depth. The read methods are `GET`, `HEAD`, `OPTIONS` and `PROPFIND`.
- Sensitive segments are write-blocked anywhere in the path: `.env`, any `*.pem` and any `.git`. So the clone of a workspace, the `.env` of the agent and TLS keys cannot be overwritten through WebDAV.

When no `/dav/` row exists, because WebDAV is not enabled, proxyd answers 404 `WebDAV not configured`. `DAV_PORT` publishes davd on the loopback of the host. A request to that port does not pass through proxyd, so no `davAllow` check runs on it.

`COPY` and `MOVE` carry a second write target in the `Destination` header, and both checks run on it too. proxyd resolves the header the way `dufs` does: a URI parse that discards the scheme and the authority, one percent-decode, and then the semantics of Rust `Path::components`. It rewrites the header to the path that it authorized before it forwards, so the backend resolves what proxyd wrote and not what the caller sent. A destination outside `/dav/`, an unparseable destination, or one with a `..` gets `400 Bad Request`.

Bare `/dav` redirects to the first group of the caller in sorted order that is not `**`, or to `root` when there is none. A blocked request logs `"dav blocked"`, or `"dav destination blocked"` or `"dav destination forbidden"` for the header, and gets `403 Forbidden`.

## Entry points

The binary is `proxyd/main.go`. It listens on `$PROXYD_LISTEN`, default `8080`.

- `/health` is the healthcheck.
- `/auth/*` is proxied to authd, so the browser sees only the public host.
- `/auth/forward` is the subrequest surface for a fronting proxy.
- `/pub/*` serves public files, from vited or through the `PUB_REDIRECT_URL` redirect.
- `/priv/*` serves private files behind auth, from vited.
- `/dav/*` is WebDAV, behind auth, scoped to the group and write-blocked.
- `/v1/proxyd_routes` is the operator REST and MCP resource.
- `/openapi.json` serves the OpenAPI document and is public.
- `/dash/proxyd/` is the operator dashboard of proxyd, with reads and forms.
- Every `proxyd_routes` row is an entry point, for example `/dash/`, `/chat/` and `/api/`.

## Dependencies

`auth` verifies ES256 JWTs against the JWKS of authd. The other packages are `audit`, `chanlib`, `core`, `groupfolder`, `obs`, `store` and `resreg`.

## Configuration

- `PROXYD_LISTEN` is the listen port. The default is `8080`.
- `VITE_ADDR` is the backend for `/pub/*` and `/priv/*`. The default is `http://vited:8080`.
- `AUTH_SECRET` keys the OAuth CSRF state. When it is unset, `requireAuth` answers 404, because the login flow cannot complete.
- `AUTHD_URL` is the authd endpoint for the ES256 JWKS, for `/v1/refresh` and for the `/auth/*` proxy.
- `AUTHD_SERVICE_KEY` is the service key of proxyd for the minting of its ES256 bearer.
- `AUTHD_SERVICE_NAME` is the name of that service. The default is `proxyd`.
- `TRUSTED_PROXIES` is a comma-separated list of CIDRs. A bare IP gets `/32` or `/128`. proxyd keeps `X-Forwarded-For` only from a peer in the list.
- `HOSTING_DOMAIN` derives the world hosts from `<world>.<HOSTING_DOMAIN>`.
- `WEB_VHOST_ALIASES` lists host-to-world entries that proxyd reads before the derivation.
- `PUB_REDIRECT_URL` is optional. When it is set and the probe succeeds, `/pub/*` returns `302` to `<url>/<rest>` and keeps the path and the query. Websocket upgrades and probe failures fall back to vited. The HEAD probe has a timeout of 2 s, and proxyd caches the result for 30 s.
- `DYNAMODB_URL` and the store credentials name the store server that holds the tables that proxyd reads and writes (`objstore/README.md`).

## Health signal

`GET /health` returns `{"ok":true}` with status 200. A request with no valid credential gets a 303 to `/auth/login` and an `auth_return` cookie. An untrusted caller on `/v1/proxyd_routes` gets 401.

## Observability

proxyd emits the two HTTP metrics that every daemon emits, at `GET /metrics` when `METRICS_ENABLED=true`. proxyd adds none of its own (`obs/README.md`).

## Files

- `main.go` holds the config, the path routing, the auth gate, the identity stamping and the host derivation.
- `routes.go` holds `Route` and its matcher.
- `resource.go` holds the `/v1/proxyd_routes` REST and MCP resource, with the store-backed route CRUD.
- `dash.go` serves `/dash/proxyd/`: the route table and its create and delete forms.
- `forward_auth.go` holds `decideForwardAuth`, the verdict for the subrequest.

## Coverage

`go test ./proxyd/ -short -cover` reports 72.4%, measured in this pass. The remaining gap is one shape: boot and the mutating dashboard forms.

- `main`, `loadConfig`, `newServer`, `loadInitialRoutes`, `routes`, `newPubRedirect` and `defaultProbe` are the wiring that listens on a port and exits non-zero. `make test-e2e` exercises them.
- `handleDashRouteCreate`, `handleDashRouteDelete` and their two helpers are the `/dash/proxyd/` forms. Tests cover the same mutations through `/v1/proxyd_routes` (`resource_test.go`), which is the handler that the forms call. What is untested is the form parse in front of it.

Tests cover routing, the auth gate, identity stamping, vhost derivation, the WebDAV write-block and forward auth.

## Related docs

- [ARCHITECTURE.md](../ARCHITECTURE.md#daemon-conventions) describes the hot-path exception.
- [SECURITY.md](../SECURITY.md) describes the transit bearer and the trust zones.
- [webd/README.md](../webd/README.md) describes the route-token surface behind `/chat/` and `/hook/`.
