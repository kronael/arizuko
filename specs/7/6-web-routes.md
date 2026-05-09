---
status: spec
---

# Agent-controlled web routing

## Problem

Agents have no way to control which web paths are public, auth-gated, denied,
or redirected. proxyd's routing is hardcoded; agents can drop files in
`/workspace/web/pub/` but can't control who can reach them or set redirects.

## Approach

A `web_routes` table in the shared DB. Agents manage it via three MCP tools.
proxyd reads it with a short in-memory cache and applies routes before the
default catch-all (auth-gated).

**Why DB not a flat file**: routes are per-group, need concurrent writes from
multiple agent containers without coordination.

**Why longest-prefix match**: agents register prefixes like `/report/` and
specific overrides like `/report/draft` — same semantics as HTTP routing.

**Default stays auth-gated**: unmatched paths still require JWT. Agents opt
paths into public/deny/redirect explicitly; no implicit exposure.

## Schema

`store/migrations/0045-web-routes.sql` — `web_routes` table:

- `path_prefix TEXT PRIMARY KEY` — URL prefix to match (e.g. `/report/`)
- `access TEXT` — `public | auth | deny | redirect`
- `redirect_to TEXT` — target URL when `access = redirect`
- `folder TEXT` — group that owns the route (audit/cleanup)
- `created_at TEXT`

## MCP tools (gated)

- `set_web_route(path, access, redirect_to?)` — upsert a route
- `del_web_route(path)` — remove a route
- `list_web_routes()` — list routes for the caller's folder

Scoped to the agent's folder: agents can only set routes they own.
Operators (`**` ACL) can set/delete any route.

## proxyd

`proxyd/main.go` — `routeCache` struct: loads all `web_routes` rows,
refreshes every 10s. Before the default catch-all, longest-prefix match
against the cache. Access actions:

- `public` → viteProxy, no auth
- `auth` → requireAuth → viteProxy
- `deny` → 403
- `redirect` → 302 to `redirect_to`

Cache is a sorted slice of `(prefix, route)` pairs; match by iterating
longest-first. Refresh runs in a background goroutine; reads use a
`sync.RWMutex`.
