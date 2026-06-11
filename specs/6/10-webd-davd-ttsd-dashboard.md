---
status: draft
depends: [1-cockpit-index]
---

# webd + davd + ttsd dashboards — thin surfaces, one spec

Architecture, routing, auth, theme: [`6/1`](1-cockpit-index.md). Three
small surfaces, one spec. Where a surface is read-only it is read-only
**because no control verb exists** — each case is named explicitly.

## Purpose

Cockpit coverage for the web edge: webd's registration health and SSE
presence; davd's file-share liveness; ttsd's backend health.

---

## webd — `/dash/webd/`

### Pages

| Page          | Content                                      |
| ------------- | -------------------------------------------- |
| `/dash/webd/` | overview: router registration, SSE hub usage |

Route tokens and `web_routes` are **routd-owned** tables: routd serves
`GET /v1/route_tokens?owner_folder=` / `DELETE /v1/route_tokens/{jid}`
([`routd/tokens_http.go`](../../routd/tokens_http.go)) and
`/v1/web_routes` ([`routd/web_routes_http.go`](../../routd/web_routes_http.go)),
and they render on routd's routes page ([`6/3`](3-routd-dashboard.md)).
webd's dash does not host them.

### Show

- **Overview** — channel registration health (the `/health` payload,
  [`webd/server.go:285`](../../webd/server.go)); SSE hub usage: active
  keys (`folder/topic`) and subscriber counts vs capacity
  (`maxHubKeys=10000`, `maxSubsPerKey=256`,
  [`webd/hub.go:11-12`](../../webd/hub.go)). Hub state is memory-only —
  exactly the live-state argument of `6/1`.

### Control

**None.** Token revocation and web-route edits live on routd's routes
page (`6/3`); webd holds no mutable runtime state of its own. No token
minting anywhere in the cockpit: creation stays with `/me/chats/new`
and the agent tools — one creation flow.

### Required `/v1` work (webd)

- `GET /v1/hub/status` — snapshot of hub keys + per-key subscriber
  counts + caps. Exposed (not just in-process) so MCP/REST/HTML stay
  one handler set (`specs/5/5`).

---

## davd — hub tile only, no `/dash/davd/`

davd is upstream `sigoden/dufs` in an alpine wrapper
([`davd/README.md`](../../davd/README.md)) — no arizuko code runs in
the container, so there is nothing to host a dashboard or a `/v1`
surface. Decision: **davd ships no `/dash/` namespace.** Its cockpit
presence is the hub tile.

- **Show** — tile health only. dufs has no `/health`; the probe is
  `GET /` (200 = dufs index up, the documented health signal). This
  needs a per-tile probe-path override in the hub — flagged to `6/2`
  as a one-field addition to the tile config.
- **Control** — **none exists.** WebDAV here is stateless per-request
  HTTP; dufs exposes no session list and no disconnect/revoke verb, and
  davd "has no notion of identity" — auth, per-group scoping, and the
  write-guard all live upstream in proxyd (`davAllow`,
  [`proxyd/main.go:738`](../../proxyd/main.go)). So "active sessions"
  and "revoke/disconnect" are **not available**, not deferred.
- **Recent writes** — `/dav/*` write traffic is observable where it is
  gated: proxyd (`6/6`), not davd.
- Wrapping dufs with a Go sidecar just to host a page fails
  minimality; revisit only if davd ever gains arizuko code for other
  reasons.

---

## ttsd — `/dash/ttsd/`, read-only

One overview page, `/dash/ttsd/`.

### Show

- Backend URL (`TTS_BACKEND_URL`, read at boot,
  [`ttsd/main.go:34`](../../ttsd/main.go)) and which probe succeeded
  (`/health` vs HEAD-root fallback, `backendUp`,
  [`ttsd/main.go:99`](../../ttsd/main.go)).
- Reachability + probe latency — wrap the existing probe to record
  duration.
- Last proxy error + timestamp — recorded in-memory by the
  `ReverseProxy.ErrorHandler` ([`ttsd/main.go:76`](../../ttsd/main.go)).
- Voice list via the existing `GET /v1/voices` forward
  ([`ttsd/main.go:50`](../../ttsd/main.go)).

### Control

**None — explicitly.** Backend selection is env-only
(`TTS_BACKEND_URL`); no runtime switch verb exists, and none is added:
infra toggles stay in env (CLAUDE.md business-vs-infra). Switching
backends = edit `.env` + restart, an operator runbook step, not a
dashboard affordance. The page states this inline so nobody hunts for
a button.

### Required `/v1` work (ttsd)

- `GET /v1/status` — `{backend, ok, probe, latency_ms, last_error,
last_error_at}` (to add; today `/health` returns only
  `ok|disconnected`, [`ttsd/main.go:85`](../../ttsd/main.go)). The
  dash page is its HTML face.
- ttsd grows `theme` + `auth` imports for the dash handlers (both
  arizuko-internal; ttsd already imports `chanlib`). Route backend
  targets ttsd's configured listen addr (`TTSD_ADDR`, default `:8880`
  — a pre-existing deviation from `:8080`, carried by the route entry
  in `template/services/ttsd.toml`).

---

## Auth

Per `6/1` for webd and ttsd: proxyd `auth: "user"` transit +
daemon-side `auth.ProxydTransit` verify of the `service:proxyd` bearer
(then trust the stamped `X-User-*`) + `auth/dashauth.go` operator gate,
CSRF on writes. Both mount it with the dash handlers (ttsd's TTS
endpoints stay unauthenticated behind proxyd as today). davd: nothing
to gate (no namespace).

## HTMX fragments

- `GET /dash/webd/x/overview` — registration + hub strip (poll 10s)
- `GET /dash/ttsd/x/status` — probe strip (poll 15s)
- `GET /dash/ttsd/x/voices` — voice list (load once)

## Non-goals

Per `6/1`. Additionally: no chat-message browsing in webd's dash (the
`/` groups page and `/me/` console already render conversations — one
renderer); no token minting; no davd session views (impossible, above);
no ttsd audio test-synthesis button (spends backend compute from an
operator page; use the agent); no SSE live-tail of hub events.

## Acceptance

- `/dash/webd/` shows hub key/subscriber counts that move when a
  `/chat/<token>/sse` stream opens and closes.
- No `/dash/webd/tokens` or `/dash/webd/routes` page exists; route
  tokens and web routes render on routd's routes page (`6/3`).
- Hub tile for davd: green when dufs serves `GET /` 200, `err` when
  the container is down — with no `/dash/davd/` route registered.
- `/dash/ttsd/` shows backend URL, latency, and last error; killing
  the kokoro container flips the page (and `/health`) to disconnected
  within one poll. No control affordance is rendered.
- All three: non-operator access → 403 theme banner (webd, ttsd) or
  proxyd denial; nothing reads a DB outside its owning daemon.
