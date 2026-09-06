---
status: shipped
shipped: 2026-08-30
depends: [28-cockpit-index]
---

# Adapter dashboard contract

**Purpose** — define the adapter dashboard ONCE: page grammar, show
matrix, health semantics. The ten channel adapters are variations on
one chanlib runtime, so they get one dashboard renderer with
per-adapter deltas. [`6/32`](32-adapter-dashboards.md) instantiates
this contract and lists only deltas. Architecture, routing, auth,
theme: [`6/28`](28-cockpit-index.md).

## One renderer in chanlib

All Go adapters share `chanlib.NewAdapterMux` for their HTTP surface.
The dashboard follows the same line: **one dashboard renderer
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
  1. **Overview** — status dot (`ok|stale|disconnected`), last inbound,
     the registered JID prefixes.
  2. **Capabilities** — the registered caps strip, one entry per verb,
     supported/unsupported.
  3. **Delta sections** — the adapter's own panels (`6/32`), label/value
     rows, rendered only for adapters that implement `DashSectioner`.

## Show

Every datum maps to state the adapter already holds. Liveness semantics
are **exactly** `chanlib/handler.go handleHealth` + `chanreg/health.go`
— the dashboard renders the same struct the `/health` endpoint serves,
never recomputes status from raw fields. One liveness computation,
three sinks (Docker healthcheck, chanreg health loop, dashboard).

| Datum           | Source                                                                                                                                     |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| status          | `/health` struct: `disconnected` (503, `isConnected()` false) > `stale` (`last_inbound_at` older than threshold) > `ok`                    |
| stale threshold | `chanlib.staleThresholds`: 5m default, `email` 10m, `reddit` 60m; stale is informational (200), never 503 — death is `isConnected()` false |
| last inbound    | `lastInboundAt()` (unix s) + `stale_seconds` from `/health`                                                                                |
| registration    | adapter-local view: name, JID prefixes, caps as registered with routd                                                                      |
| capabilities    | probed from the verb (`isStubVerb`), the same check `CapImplReport` uses                                                                   |

Registry-side state — `chanreg.Entry.HealthFails`, auto-deregister
after 3 fails, registered URL/token — lives in **routd** and is not
duplicated on the adapter page.

## Control

whapd's pair endpoints (`GET /v1/pair/status`, `POST /v1/pair/start`)
are whapd-local; no other adapter carries a control. An affordance with
no underlying mechanism is **absent from the page**, not greyed out.
Adapters serve no MCP surface, so no MCP face is added (the platform's
MCP face is routd's).

## Auth

Per [`6/28`](28-cockpit-index.md): proxyd `auth: "user"` transit +
daemon-side `auth.ProxydTransit` verify of the `service:proxyd` bearer
(then trust the stamped `X-User-*`) + `auth/dashauth.go` operator
gate, CSRF on writes. Each adapter ships its own `/dash/<adapter>/`
entry in `template/services/<adapter>.yaml`, beside its `.yml`
compose fragment (root `CLAUDE.md` "Adding a channel adapter").

**TS adapters (whapd, twitd)** re-implement the gate in TS — verify the
`service:proxyd` bearer, then trust the stamped
`X-User-Sub`/`X-User-Groups` — plus the operator check, equivalent to
`auth/dashauth.go`; their existing adapter API keeps verifying only
routd SERVICE tokens (`whapd/src/auth.ts`, `twitd/src/auth.ts`).

## Acceptance

- `GET /dash/<adapter>/` renders for every adapter that mounts the
  contract; status dot agrees with `GET /health` for the same instant
  (ok, stale incl. `stale_seconds`, disconnected).
- Capabilities render from the probe, never from config; no field is
  computed twice.
- Non-operator caller gets the themed 403 banner; unsigned direct calls
  to dash routes are rejected.
- Registry-side fields do not appear on the adapter page.

## Build state (2026-08-30) — SHIPPED

The renderer is `chanlib/dash.go`, mounted inside `NewAdapterMux`, so all eight
Go adapters (slakd, teled, discd, emaid, bskyd, reditd, linkd, gitd) serve
`/dash/<adapter>/` with **no per-adapter code**. Every fact the page shows is
already a parameter of that constructor, which is why the contract collapses to
one mount rather than eight implementations.

Decisions worth keeping:

- **Three statuses, not two.** `stale` is a live process whose platform link has
  gone quiet — the runbook's "healthchecks green but the agent doesn't reply"
  case. It renders as a WARNING, not an error: the process is up, so the
  operator's next step is the platform link, not a restart. The threshold is
  read from the same `staleThresholds` table `/health` uses, so the page and the
  healthcheck cannot disagree about when an adapter went quiet.
- **Capabilities are probed, not declared.** The strip runs `isStubVerb` against
  each gated verb — the same check `CapImplReport` uses — because a cap read
  from config is a claim and this page exists to show what the adapter does.
- **Gating differs from the core daemons, and must.** An adapter verifies no
  tokens and holds no keyset, so the nil-`KeySet` form falls OPEN, and proxyd's
  `user` route authenticates a person rather than an operator. Adapters gate on
  the stamped operator scope, enforcing whenever `AUTHD_URL` is set.
- **Registration is the manifest.** Each adapter ships
  `template/services/<name>.yaml` with its `/dash/<name>/` route gated by the
  adapter's OWN credential, so the route — and therefore the hub tile — exists
  exactly when the adapter is deployed.

**The two TypeScript adapters** (2026-08-30): whapd and twitd cannot import
chanlib, so each carries `src/dash.ts` — the same contract, a second
implementation, which is the cost this spec was written to make payable. They
match the Go renderer on the three states, the stale-warns rule, the
fail-closed scope parse and the AUTHD_URL gate.

Two deltas are forced rather than chosen, and are stated in both files:

- **The capability strip is DECLARED, not probed.** Go's `isStubVerb` calls the
  verb and watches for `Unsupported`; a TS adapter's verbs are switch arms, not
  values, so there is nothing to call. `supportedVerbs` is a claim that must be
  edited when a case is added — the Go side cannot drift this way and this one
  can.
- **The theme is inlined**, because `theme.Page` is a Go package.

All ten adapters serve `/dash/<adapter>/`, each registered by its own
`template/services/<name>.yaml` route gated on the adapter's credential.
