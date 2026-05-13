---
status: shipped
---

# Slink — agent-built chat pages

Extension of [W-slink.md](W-slink.md). That spec defines the round-handle
JSON protocol. This one defines the infrastructure contracts that let agents
and third-party pages build on top of it.

**Implementation guide lives in `ant/skills/slink/SKILL.md`** — agents learn
the POST → SSE pattern there and write their own pages.

## Problem

The current `/slink/{token}` GET route serves a hard-coded HTML page. POST
returns an HTML fragment by default; JSON is opt-in via `Accept`. This means:

1. Every product that wants a branded chat UI has to fight the default.
2. Third-party pages embedding the API hit CORS.
3. There is no config bootstrap endpoint — callers must hard-code paths.

## Infrastructure changes (webd)

### JSON as default POST response

POST `/slink/{token}` responds JSON by default (as specified in W-slink.md
§POST shape). `Accept: text/html` stays for the legacy HTMX fragment path
used by the built-in chat page. New callers get JSON with no `Accept` header.

### Chat page at `/slink/{token}/chat`

`GET /slink/{token}` → 301 to `/slink/{token}/chat`.
`GET /slink/{token}/chat` → the default minimal chat page (moved from root).

Frees the root URL for future use (a landing page or JSON config); keeps the
chat page reachable at a stable, linkable URL.

### Config endpoint

`GET /slink/{token}/config` — JSON bootstrap, no auth:

```json
{
  "token": "646a...",
  "folder": "acme/support",
  "name": "Support",
  "endpoints": {
    "post": "/slink/{token}",
    "stream": "/slink/{token}/{turn_id}/sse",
    "status": "/slink/{token}/{turn_id}/status"
  }
}
```

Useful for pages that can't bake the token and paths in at build time.

### CORS on `/slink/*`

webd emits `Access-Control-Allow-Origin: *` on all `/slink/*` responses so
pages hosted on third-party origins can call the JSON API directly. Scope is
`/slink/*` only — nothing else in webd is affected. proxyd already strips
identity headers on inbound; CORS adds no new trust surface (the token is
already public).

## What changes

| File             | Change                                                                                           |
| ---------------- | ------------------------------------------------------------------------------------------------ |
| `webd/slink.go`  | POST default → JSON; `GET /{t}` → 301 to `/chat`; add `/chat` + `/config` handlers; CORS headers |
| `proxyd/main.go` | `/slink/*/config` already covered by `/slink/*` public rule; no change                           |

Fragment handler stays — used by the chat page's own HTMX calls. Remove when
the built-in chat page migrates off HTMX.

## Non-goals

- No widget file shipped by arizuko. Agents write their own pages using the
  `slink` skill as a guide.
- No CDN hosting. Pages are self-hosted on the arizuko instance under
  `/workspace/web/pub/<name>/`.
- No per-token UI customization in webd — that's the agent's job.

## Relationship to W-slink.md

W-slink.md owns the round-handle protocol (POST shape, steer, snapshot,
status, SSE, MCP). This spec owns the browser infrastructure (CORS, /chat
page, /config endpoint, JSON-default POST). No duplication.
