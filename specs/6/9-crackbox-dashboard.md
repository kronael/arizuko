---
status: draft
depends: [1-cockpit-index]
---

# crackbox dashboard — egress policy, blocked attempts, registrations

Architecture, routing, auth, theme: [`6/1`](1-cockpit-index.md). This
spec adds only crackbox's pages + show/control matrix, and resolves the
two tensions unique to crackbox: orthogonality and ports.

## Purpose

Observe and control the live egress enforcement point: registered
workloads and their allowlists, and what got blocked.

## What exists vs proposed (honesty section)

- **Shipped**: the `egred` proxy half — forward proxy `:3128`,
  transparent `:3127`, DNS `:53`, admin API `:3129` with
  `POST /v1/register`, `POST /v1/unregister`, `GET /v1/state`,
  `GET /health` ([`crackbox/pkg/admin/api.go:78-85`](../../crackbox/pkg/admin/api.go)).
  runed registers each spawn's IP+allowlist and unregisters on exit
  ([`container/runner.go:188`](../../container/runner.go),
  [`container/egress.go:110`](../../container/egress.go)); routd
  resolves the per-folder allowlist at dispatch
  ([`routd/dispatch.go:391`](../../routd/dispatch.go)).
- **Conditional**: the crackbox container only runs on instances with
  egress isolation enabled (`EGRESS_CRACKBOX`,
  [`compose/compose.go:427`](../../compose/compose.go)). No crackbox
  service → no `[[proxyd_route]]` entry → no hub tile (the route entry
  IS the registration, `6/1`).
- **Planned, out of scope here**: the KVM host half (`pkg/host/`,
  specs/11/12) — nothing to dashboard yet.
- **Proposed by this spec**: everything under Pages, plus the
  `/v1/denials` read model and one proxyd route field.

## Orthogonality + auth decisions

Crackbox imports no arizuko-internal subpackage (the README grep,
[`crackbox/README.md`](../../crackbox/README.md) §Orthogonality). Two
consequences, decided here:

1. **Theme**: crackbox imports `github.com/kronael/arizuko/theme` —
   a stdlib-only leaf ([`theme/theme.go`](../../theme/theme.go) imports
   only `html`, `html/template`). Amend the README grep note to declare
   `theme` a shareable leaf alongside `crackbox/pkg/...`. Rejected:
   copying the CSS (two renderers drift); serving the page from dashd
   (violates `6/1` per-daemon ownership).
2. **Daemon-side auth gate**: crackbox cannot import `arizuko/auth`
   (it's in the orthogonality grep), so it cannot verify proxyd's
   signed headers. Instead the gate is crackbox's **existing** admin
   bearer check (`authOK`, constant-time,
   [`crackbox/pkg/admin/api.go:49`](../../crackbox/pkg/admin/api.go)):
   proxyd's route entry injects `Authorization: Bearer
$CRACKBOX_ADMIN_SECRET` on forward (new `Route` field, below). Gate
   1 stays the standard proxyd transit, with `auth: "operator"`
   ([`proxyd/routes.go:12`](../../proxyd/routes.go) already accepts
   it). Rejected: hand-rolling an HMAC verifier inside crackbox
   (duplicates `auth/middleware.go` — drift), trusting the docker
   network (agent containers share the egress network with crackbox;
   that's exactly why `CRACKBOX_ADMIN_SECRET` exists).

`/dash/crackbox/` is served from the **admin listener (`:3129`)**, the
mux that already owns the same data. The proxyd route backend targets
`<crackbox-container>:3129` — a documented deviation from the `:8080`
convention (crackbox's ports predate it and are part of its external
contract).

## Pages

| Page                      | Content                                      |
| ------------------------- | -------------------------------------------- |
| `/dash/crackbox/`         | overview: listeners, proxy self-test, counts |
| `/dash/crackbox/registry` | registered workloads + per-IP allowlists     |
| `/dash/crackbox/denials`  | recent blocked attempts                      |

## Show

- **Overview** — proxy-listener self-test (the `/health` TCP dial,
  [`crackbox/pkg/admin/api.go:137`](../../crackbox/pkg/admin/api.go));
  which listeners are enabled (forward `:3128`, transparent `:3127`,
  DNS `:53` — config per README table); registry persistence on/off
  (`CRACKBOX_STATE_PATH`); registration count; denials in the last
  hour. All in-process — the dash handlers live beside the registry.
- **Registry** — one row per registered workload from
  `Registry.Snapshot()`
  ([`crackbox/pkg/admin/registry.go:149`](../../crackbox/pkg/admin/registry.go)):
  source IP, `id` label (arizuko sets it to the agent's folder), the
  allowlist. Default-deny: an IP not in this table can reach nothing.
- **Denials** — recent blocked attempts: timestamp, source IP, `id`,
  target host, listener (`connect|http|transparent|dns`). Today these
  are slog-only ([`pkg/proxy/proxy.go:67,109`](../../crackbox/pkg/proxy/proxy.go),
  [`pkg/proxy/transparent.go:83`](../../crackbox/pkg/proxy/transparent.go),
  [`pkg/dns/server.go:130`](../../crackbox/pkg/dns/server.go)) — the
  read model is the one `/v1` addition below.

## Control

| Affordance               | Verb                                                                                                                          | Danger                                                            |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| unregister / clear stale | `POST /v1/unregister` `{ip}` (exists, [`api.go:109`](../../crackbox/pkg/admin/api.go))                                        | `.btn-danger` — on a LIVE workload this cuts all egress instantly |
| edit live allowlist      | `POST /v1/register` `{ip,id,allowlist}` (exists; `Set` overwrites, [`registry.go:111`](../../crackbox/pkg/admin/registry.go)) | confirm — see ephemerality note                                   |

- **Normal lifecycle needs neither**: runed registers per spawn and
  `defer`-unregisters on exit. Manual unregister exists for stale
  entries left by a runed crash (the "clear stale registration" brief);
  the registry page sorts stale-looking entries (no matching live
  container is a `6/4` concern — here, operator judgment) first.
- **Ephemerality, stated in the UI**: a live-allowlist edit lasts until
  the next spawn re-registers from routd-resolved policy. Persistent
  per-folder egress policy is routd's `network_rules`
  ([`routd/network.go:15,25,52`](../../routd/network.go)) — edited on
  routd's dashboard (`6/3`), **not here**. Crackbox's dash shows live
  enforcement state; routd's dash owns policy. One renderer each.
- No "add allow/deny rule" affordance: crackbox has no persistent rule
  store beyond registrations; default-deny is structural.

## Required `/v1` work

- **`GET /v1/denials?limit=`** (crackbox, to add) — in-memory ring
  buffer (last 256) of deny events `{ts, src, id, host, listener}`,
  appended at the three existing deny sites + DNS. Read-only; served
  on the admin mux like `/v1/state`. RAM-only (restarts drop it) —
  journald keeps the forensic record; this is an operator glance, not
  an audit log.
- **proxyd `Route.InjectAuthorization`** (to add,
  [`proxyd/routes.go:18`](../../proxyd/routes.go)) — optional static
  bearer attached on forward, set from env in the route TOML. Needed
  for the daemon-side gate above; also reusable by any future
  orthogonal sibling.
- Ship `template/services/crackbox.toml` carrying the
  `[[proxyd_route]]` for `/dash/crackbox/` → `:3129`, `auth =
"operator"`, gated by `EGRESS_CRACKBOX` (the adapter pattern,
  `specs/5/35`).

## Auth

Gate 1 per `6/1` but `auth: "operator"` at proxyd (crackbox cannot run
the daemon-side operator decision itself). Gate 2: the injected admin
bearer verified by `authOK` — covering the whole `/dash/crackbox/`
namespace, reads included. CSRF: same-origin check hand-rolled in the
dash handlers (cannot import `auth/dashauth.go`; it's ~5 lines and the
mutating surface is two forms). Instances running crackbox MUST set
`CRACKBOX_ADMIN_SECRET` — an empty secret already logs a warning
(README); with a dashboard exposed it becomes a hard requirement,
enforced at route-TOML level (`gated_by` the secret var).

## HTMX fragments

- `GET /dash/crackbox/x/overview` — listener + count strip (poll 10s)
- `GET /dash/crackbox/x/registry` — registry table body
- `GET /dash/crackbox/x/denials?limit=` — denial rows (poll 10s)
- `POST /dash/crackbox/x/unregister` / `POST /dash/crackbox/x/register`
  — control forms; in-process calls to `reg.Remove`/`reg.Set`, return
  the refreshed registry table

## Non-goals

Per `6/1`. Additionally: no persistent egress-policy editing (routd's,
`6/3`); no KVM/host-half views (nothing shipped); no per-request
traffic log (denials only — allowed traffic is volume, not signal); no
TLS/MITM inspection views.

## Acceptance

- On an `EGRESS_CRACKBOX` instance, the hub shows a crackbox tile;
  `/dash/crackbox/` renders listeners + registration count matching
  `crackbox state`. On a non-crackbox instance: no tile, no route.
- A running agent spawn appears in the registry with its folder as
  `id` and the routd-resolved allowlist; it disappears when the run
  ends.
- `curl https://blocked.example` from inside a sandboxed container →
  a row appears on the denials page within one poll.
- Unregister on a live entry asks for confirmation; after it, the
  workload's egress is denied (next request shows in denials).
- Requests to `/dash/crackbox/` without operator transit are rejected
  by proxyd; direct container-network requests without the admin
  bearer get 401 from `authOK`.
- The orthogonality grep in `crackbox/README.md` still returns empty
  (with `theme` added to its declared-leaf note).
