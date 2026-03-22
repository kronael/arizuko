---
status: planned
---

# webd — Web Serving Layer

webd is the public-facing HTTP daemon for an arizuko instance. It sits
behind the Hetzner LB (or any reverse proxy), owns the external port,
and routes requests to internal services.

## webd

Standalone daemon (`webd/main.go`). Listens on `WEB_PORT`.

### Routing (evaluated in order)

1. `WEB_REDIRECTS` prefixes — JSON map of path prefix → upstream URL.
   Checked first. Enables routing `/agents/` to a per-world custom
   server without code changes.
2. `/dash/*` → proxy to `DASH_ADDR` (dashd operator dashboard).
3. `/*` → proxy to `VITE_ADDR` (Vite MPA container).

Rules are fixed; only the redirect table is configurable.

### WEB_REDIRECTS

```
WEB_REDIRECTS='{"\/agents\/": "http://custom-vite:3001"}'
```

JSON map. Keys are path prefixes (longest match wins). Values are
upstream base URLs. webd strips no path — the full request path is
forwarded as-is.

Future: could be read from `WEB_DIR/web-redirects.json` for easier
editing without restart.

### Auth

- `/dash/*` — required. webd validates bearer token or session cookie
  before proxying. 401 if missing or invalid.
- `/*` — controlled by `WEB_PUBLIC` (bool). If false, same auth check
  applies. If true, requests pass through unauthenticated.

Auth token source: shared secret with dashd (env var). Session cookie
set by dashd login flow, validated by webd on subsequent requests.

Open question: consolidate auth entirely at webd level (webd issues
sessions, dashd trusts forwarded identity header) vs current model
where dashd has its own auth. Consolidation simplifies dashd but
requires webd to know about login endpoints.

### Logging

All requests logged: timestamp, method, path, upstream target, status
code, latency. Unix log format.

### Env vars

| Var             | Default | Description                             |
| --------------- | ------- | --------------------------------------- |
| `WEB_PORT`      | —       | External listen port                    |
| `DASH_ADDR`     | —       | dashd base URL (e.g. http://dashd:8082) |
| `VITE_ADDR`     | —       | Vite base URL (e.g. http://vited:5174)  |
| `WEB_PUBLIC`    | false   | Whether /\* is publicly accessible      |
| `WEB_REDIRECTS` | `{}`    | JSON map of prefix → upstream URL       |

## vited — Vite MPA Container

One Vite instance per arizuko instance. Runs as a container named
`vited`. Internal only — not exposed externally.

Serves `WEB_DIR` (e.g. `/srv/data/REDACTED/web/`) as a
multi-page app (`appType: "mpa"` in vite.config.ts).

Port: `VITE_PORT = WEB_PORT + 1`. Not exposed outside compose network.

### Directory layout

```
WEB_DIR/
  index.html          → /
  <world>/
    index.html        → /<world>/
    assets/           → /<world>/assets/
```

World web content at `WEB_DIR/<world>/index.html` → accessible at
`/<world>/`. No auth at Vite level — webd handles auth before proxying.

### vite.config.ts

Location: open question (see below). At minimum the container image
ships a default config. Per-instance overrides can live in `WEB_DIR/`.

### Static vs dev Vite

Open question: dev Vite (`vite serve`) has HMR and no build step but
is heavier. Production builds (`vite build`) are lighter but require
a rebuild pipeline when content changes. Current assumption: dev mode
for now, revisit when content update frequency warrants it.

## Compose Wiring

```yaml
webd:
  image: arizuko-webd
  ports:
    - '${WEB_PORT}:${WEB_PORT}'
  environment:
    WEB_PORT: ${WEB_PORT}
    DASH_ADDR: http://dashd:${DASH_PORT}
    VITE_ADDR: http://vited:${VITE_PORT}
    WEB_PUBLIC: ${WEB_PUBLIC:-false}
    WEB_REDIRECTS: ${WEB_REDIRECTS:-{}}

vited:
  image: arizuko-vited
  volumes:
    - ${WEB_DIR}:/web:ro
  environment:
    VITE_PORT: ${VITE_PORT}
  # no ports — internal only
```

gated loses `WEB_PORT`. gated is API only (`API_PORT`, default 8081).

## Open Questions

**Static vs dev Vite in production** — `vite serve` (dev) works out
of the box; `vite build` + static file server is lighter but needs
a rebuild trigger when `WEB_DIR` changes. Leaning dev for now.

**Auth consolidation** — dashd currently has its own auth. Consolidate
at webd (webd issues sessions, injects `X-User` header, dashd trusts
it) or keep split? Consolidated is simpler long-term; split is less
invasive now.

**Per-world Vite isolation** — when does a world warrant its own Vite
container vs just a subdirectory in the shared instance? Cost: one
process per world. Benefit: independent deploys, different configs.
`WEB_REDIRECTS` enables this without code changes.

**vite.config.ts location** — in the container image (shared default)
or per-instance in `WEB_DIR/vite.config.ts` (overrideable)? Both have
merit; shipping a default in-image with an optional override in
`WEB_DIR` is the middle ground.

**WebDAV** — kanipi had `/dav/<group>/` for file access. Include
in webd as another routing rule (webd proxies to a dawd daemon) or
ship dawd separately and let operators wire it via `WEB_REDIRECTS`?
`WEB_REDIRECTS` makes dawd a drop-in without touching webd code.

**Web channel** — kanipi had `/_REDACTED/message` (now `/slink/`) for browser→agent
messaging. Include in webd (webd accepts POST, writes to store) or
implement as a separate channel daemon registered via chanreg? The
chanreg model is cleaner and consistent with other adapters.
