---
status: draft
depends: [1-cockpit-index]
---

# Adapter dashboard contract

**Purpose** — define the adapter dashboard ONCE: page grammar, show
matrix, control verbs, health semantics. The ten channel adapters are
variations on one chanlib runtime, so they get one dashboard renderer
with per-adapter deltas. `6/12`–`6/14` instantiate this contract and
list only deltas. Architecture, routing, auth, theme: [`6/1`](1-cockpit-index.md).

## One renderer in chanlib

All Go adapters already share `chanlib.NewAdapterMux` for their HTTP
surface. The dashboard follows the same line: **one dashboard renderer
implemented in `chanlib`, mounted by every Go adapter**, parameterized
by the adapter's name, caps, and an optional list of extra sections
(whapd pairing pane, reditd cursor table, …). Per-adapter dashboard
code is the delta sections only — never a second copy of the shared
grammar (CLAUDE.md "One renderer, many sinks").

The two TypeScript adapters (whapd, twitd) cannot import `chanlib`;
they re-implement this contract in their own source (see "TS
adapters" below). That cost is why the contract is written down.

## Pages

One page per adapter — adapters are small; depth comes from sections,
not page count.

- `/dash/<adapter>/` — single page with sections, top to bottom:
  1. **Overview** — status dot (`ok|stale|disconnected`), authenticated
     identity, transport one-liner, registered caps strip.
  2. **Connection / session** — platform link detail, persisted session
     state, identity fields.
  3. **Stream / poll health** — last inbound, staleness, poll/stream
     specifics.
  4. **Rate limits** — only rendered when the adapter holds rate-limit
     state (else omitted, not empty).
  5. **Recent errors** — last N transport errors (ring buffer).
  6. **Capabilities** — the registered caps map, one row per verb,
     supported/unsupported.
  7. **Controls** — the verb buttons (below), dangerous ones
     `.btn-danger` with confirm.

## Show

Every datum maps to state the adapter already holds (or the "Required
work" additions). Liveness semantics are **exactly**
`chanlib/handler.go handleHealth` + `chanreg/health.go` — the
dashboard renders the same struct the `/health` endpoint serves, never
recomputes status from raw fields. One liveness computation, three
sinks (Docker healthcheck, chanreg health loop, dashboard).

| Datum           | Source                                                                                                                                                 |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| status          | `/health` struct: `disconnected` (503, `isConnected()` false) > `stale` (`last_inbound_at` older than threshold) > `ok`                                |
| stale threshold | `chanlib.staleThresholds`: 5m default, `email` 10m, `reddit` 60m; `strictStale` set (slack) makes stale a 503                                          |
| identity        | what the adapter holds in memory after auth (phone JID, bot username, `auth.test` user+team, account handle, DID, reddit username, URN, email account) |
| transport       | static per adapter: websocket / long-poll / webhook push / IMAP IDLE / HTTP poll + interval                                                            |
| last inbound    | `lastInboundAt()` (unix s) + `stale_seconds` from `/health`                                                                                            |
| last outbound   | NEW — chanlib mux records last successful verb dispatch (see Required work)                                                                            |
| registration    | adapter-local view: name, JID prefixes, caps map as registered with routd                                                                              |
| rate limits     | adapter-held state only (whapd pair bucket, reditd Retry-After sleep, discd retry counter); omitted otherwise                                          |
| recent errors   | NEW — chanlib ring buffer of last N transport/verb errors (see Required work)                                                                          |

Registry-side state — `chanreg.Entry.HealthFails`, auto-deregister
after 3 fails, registered URL/token — lives in **routd** and is shown
on routd's channel-registry page ([`6/3`](3-routd-dashboard.md)). The
adapter overview links there; it does not duplicate it.

## Control

Canonical verb names, defined once. A group spec maps each adapter to
the subset that is real for it; an affordance with no underlying
mechanism is **absent from the page**, not greyed out.

| Affordance    | Verb                     | Exists today       | Danger                                                                             |
| ------------- | ------------------------ | ------------------ | ---------------------------------------------------------------------------------- |
| reconnect     | `POST /v1/reconnect`     | no — Required work | no                                                                                 |
| refresh auth  | `POST /v1/auth/refresh`  | no — Required work | no                                                                                 |
| pair status   | `GET /v1/pair/status`    | yes (whapd only)   | no                                                                                 |
| start pairing | `POST /v1/pair/start`    | yes (whapd only)   | no                                                                                 |
| reset session | `POST /v1/session/reset` | no — Required work | **yes — `.btn-danger`**, confirm; destroys persisted creds, forces re-auth/re-pair |

Semantics:

- **reconnect** — tear down and re-establish the platform link: cancel
  - redial stream/gateway/IMAP, restart the poll loop with an
    immediate poll. Idempotent; safe while connected.
- **refresh auth** — re-run the adapter's existing token-refresh /
  auth-probe path. Only offered where that path exists in code.
- **reset session** — delete persisted session credentials (whapd
  Baileys creds, twitd cookies) so the next connect starts a fresh
  auth/pair flow. Dangerous: the account goes offline until an
  operator completes pairing/login.
- **pause outbound** — NOT offered. No adapter has a pause mechanism;
  outbound buffering during disconnect lives in routd's
  `chanreg.HTTPChannel` outbox and belongs to `6/3` if anywhere.
- **refresh registration** — NOT offered as a distinct verb;
  registration re-runs on adapter restart and `reconnect` does not
  touch it. Forcing re-registration is `routd`'s side (`6/3`).

## Required `/v1` + chanlib work

Shared additions, implemented once in chanlib (TS adapters mirror):

- `lastOutboundAt` — mux records unix seconds of last successful
  outbound verb (`writeBotResult` success path).
- error ring — bounded in-memory ring (last ~50) of transport + verb
  errors with timestamp and verb name; no persistence.
- `GET /v1/status` — one read endpoint returning the dashboard's
  superset struct: the `/health` fields + identity + transport +
  `last_outbound_at` + rate-limit state + error ring. The page and its
  partials render this struct; group specs extend it with delta
  fields.
- `POST /v1/reconnect`, `POST /v1/auth/refresh`,
  `POST /v1/session/reset` — chanlib registers each only when the
  adapter passes the corresponding hook (nil hook → no route, no
  button). Existing whapd pair endpoints stay whapd-local.
- dashboard mount — chanlib serves `/dash/<adapter>/` + partials from
  the same mux, behind the dash gate.

These verbs are adapter-local REST for operators; adapters serve no
MCP surface, so no MCP face is added (the platform's MCP face is
routd's — `specs/5/5` uniformity applies to resource daemons, not
adapter control verbs).

## Auth

Per [`6/1`](1-cockpit-index.md): proxyd `auth: "user"` transit +
daemon-side `RequireSigned` + `auth/dashauth.go` operator gate, CSRF
on writes. Each adapter ships its own `[[proxyd_route]]` for
`/dash/<adapter>/` in `template/services/<adapter>.toml`.

Two callers, one function: the existing `/v1` verb routes keep
`chanlib.Auth` (session-token bearer, called by routd); the dashboard
buttons post to dash-gated partial routes that invoke the **same
function** in-process. One implementation per control, two auth faces.

**TS adapters (whapd, twitd)** re-implement the gate: verify the
proxyd-signed `X-User-Sub`/`X-User-Groups`/`X-User-Sig` headers +
operator check + same-origin CSRF, equivalent to `auth/dashauth.go`.
Theme: the hub.css content is vendored from `theme/` into the TS
images at build time — one source in `theme/`, copied by the image
build, never hand-edited downstream.

## HTMX fragments

Under `/dash/<adapter>/x/...`:

- `GET x/status` — overview + connection sections (poll `every 5s`).
- `GET x/errors` — error ring table.
- `GET x/ratelimit` — rate-limit section (only mounted when present).
- `POST x/reconnect`, `POST x/auth-refresh`, `POST x/session-reset` —
  dash-gated faces of the `/v1` controls; respond with the refreshed
  status fragment.

Group specs add delta fragments (e.g. whapd `x/pair`).

## Non-goals

`6/1` non-goals apply. Additionally, for all adapters:

- No message browsing — messages live in routd (`6/3`); the adapter
  page shows transport health, not content.
- No platform-side administration (Slack app config, Discord guild
  management, subreddit settings).
- No editing of connection env (tokens, hosts, intervals) — infra
  config stays in `.env` (CLAUDE.md "Business vs infra").
- No outbound queue control (routd-side, `6/3`).
- No historical error/uptime storage — the error ring is in-memory and
  dies with the process.

## Acceptance

- `GET /dash/<adapter>/` renders for every adapter that mounts the
  contract; status dot agrees with `GET /health` for the same instant
  (ok, stale incl. `stale_seconds`, disconnected).
- Identity, transport, last inbound/outbound, caps render from
  `GET /v1/status`; no field is computed twice.
- Every rendered control button maps to a registered `/v1` verb; an
  adapter without a hook shows no button and serves 404 on the route.
- `session-reset` requires confirm, renders `.btn-danger`, and after
  firing the page shows `disconnected` until re-auth completes.
- Non-operator caller gets the themed 403 banner on page and partials;
  unsigned direct calls to dash routes are rejected.
- routd's channel-registry page is linked from the overview;
  registry-side fields do not appear on the adapter page.
