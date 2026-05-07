---
status: spec
---

# Slink widget — embeddable JS client

Extension of [W-slink.md](W-slink.md). That spec defines the round-handle JSON
protocol; this one defines the embeddable browser surface built on top of it.

## Problem

The current `/slink/{token}` page is self-contained: HTML, CSS, and JS are all
inlined. The POST handler returns an HTMX fragment by default; JSON is opt-in
via `Accept: application/json`. This couples the transport protocol to the
widget implementation — any third-party site that wants to embed a chat widget
must either scrape fragments or fight the content-type negotiation.

Concretely, the things that are wrong:

1. POST `/slink/{token}` returns HTML by default. The JSON API defined in
   W-slink.md is the canonical protocol but isn't the default.
2. The chat page lives at the root slink URL, so there's no clean endpoint a
   third party can point users at.
3. There is no embeddable widget — no script tag an operator can drop into
   their own site.

## Approach

Three changes, independent of each other:

### 1. JSON is the default

POST `/slink/{token}` responds with JSON by default (as already specified in
W-slink.md §POST shape). `Accept: text/html` is the opt-in path for legacy
fragment callers. Fragment support stays for backward compat but is no longer
the default.

### 2. Chat page at `/slink/{token}/chat`

`GET /slink/{token}` — redirect (301) to `/slink/{token}/chat`.
`GET /slink/{token}/chat` — the standalone chat HTML page (what is today's
`GET /slink/{token}`). Moving it here keeps the root URL free for future use
(a landing page, API docs, or redirect) while keeping the chat page reachable
at a stable URL.

### 3. Config endpoint + embeddable widget

`GET /slink/{token}/config` — JSON bootstrap. No auth required; the token is
already public. Returns:

```json
{
  "token": "646a1ee586e63313...",
  "folder": "acme/support",
  "name": "Support",
  "endpoints": {
    "post": "/slink/{token}",
    "stream": "/slink/{token}/{turn_id}/sse",
    "status": "/slink/{token}/{turn_id}/status"
  }
}
```

Gives any client the minimum it needs without hardcoding paths.

`GET /pub/slink-widget.js` — self-contained embeddable widget. Served as a
static asset from webd (no auth, no token in path). One script tag is the
entire integration:

```html
<script
  src="https://your-instance.example.com/pub/slink-widget.js"
  data-token="646a1ee586e63313"
  data-container="#chat-root"
></script>
```

Widget behaviour:

1. On load, fetches `/slink/{token}/config` to get the folder name.
2. Creates a minimal chat UI in `data-container` (or `document.body` if
   omitted): input textarea, send button, message thread div.
3. On send, POST JSON to `/slink/{token}`. Receives `{turn_id, ...}`.
4. Opens SSE on `/slink/{token}/{turn_id}/sse`. Appends frames as they arrive.
   Closes when `round_done` fires.
5. Reconnects on disconnect using `Last-Event-Id`.

JS init API (for callers who prefer programmatic control):

```javascript
window.SlinkWidget.init({
  token: '646a1ee586e63313',
  base: 'https://your-instance.example.com', // optional, infers from script src
  container: document.querySelector('#chat-root'),
  theme: 'light', // 'light' | 'dark' | 'auto'
});
```

## CORS

slink endpoints must emit `Access-Control-Allow-Origin: *` so embedded widgets
on third-party origins can call the JSON API. Scope to `/slink/*` only — the
rest of webd stays unaffected. proxyd already strips identity headers on
incoming requests; CORS adds no new trust surface since the token is already
public.

`/pub/slink-widget.js` is served without CORS restrictions (it's a script, not
XHR).

## What changes

| File                   | Change                                                                                                                                                          |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `webd/slink.go`        | POST default → JSON; `GET /slink/{t}` → 301 to `/chat`; add `GET /slink/{t}/chat` handler; add `GET /slink/{t}/config` handler; emit CORS headers on `/slink/*` |
| `webd/slink_widget.js` | New file — the embeddable widget source                                                                                                                         |
| `webd/server.go`       | Register `/pub/slink-widget.js` route (static serve, no auth)                                                                                                   |
| `proxyd/main.go`       | `/slink/*/config` already public (covered by `/slink/*` rule); no change                                                                                        |

Fragment handler (`Accept: text/html`) stays in `webd/slink.go` — it's used by
the chat page's own HTMX calls and by any existing integrations. Remove it only
when the chat page (`/slink/{t}/chat`) migrates off HTMX internally.

## Non-goals

- No widget CDN — the script is self-hosted on the arizuko instance.
- No OAuth popup in the widget — anon identity is enough for the public embed
  use case. JWT bearer can be injected via `window.SlinkWidget.init({ jwt })`.
- No per-token customization (colors, logo) in v1 — that's a product layer on
  top.
- Existing per-folder SSE stream (`GET /slink/stream`) stays unchanged.

## Relationship to W-slink.md

W-slink.md owns the round-handle protocol (POST, steer, snapshot, status, SSE,
MCP). This spec owns the browser surface (chat page, widget, config endpoint,
CORS). No duplication — cite W-slink.md for protocol details, this spec for
embed details.
