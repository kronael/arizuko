# onbod

Onboarding daemon: gated admission queue + OAuth link.

## Purpose

Self-service onboarding. An unrouted JID receives a one-time PAIRING link
(`specs/5/31`), proxyd's OAuth flow confirms identity (authd), and webd's
`/pair/{token}` writes the `acl_membership` edge; the user continues to
`/onboard` to choose which world the chat belongs to. Optional per-gate daily
limits throttle admission. onbod also creates a user world via
`container.SetupGroup` on invite redemption.

## Responsibilities

- Poll `awaiting_message` rows; mint a pairing link (`store.IssuePairingLink`,
  `owner_folder` NULL) and send it via routd's outbound API
  (`promptUnprompted`).
- Observe redemption: each tick matches unclaimed rows against
  `acl_membership WHERE added_by='pairing'` and runs admission over the
  committed edge (`observePairings` → `admitJID`).
- Serve `/onboard` landing, world picker, username picker, world-creation page;
  redirect to `/auth/login` for OAuth.
- Match users to gates (github-org, google-domain, catch-all); enforce per-gate
  daily limits.
- Promote queued users to `approved` via `admitFromQueue` loop (~60s).
- Let an `approved` caller who administers no world create a top-level one
  (`mayCreateFirstWorld`, spec `5/18` step 8) — **only while at least one
  enabled gate exists.** With no gate, `admitJID` approves every paired
  identity, so the gate is what keeps world creation invite-only by default;
  configuring one is the operator's opt-in to self-serve signup.

**onbod writes no `acl_membership` edge.** Identity pairing
(`specs/5/31-identity-pairing.md`) owns that write, in routd's transaction,
stamped `added_by='pairing'` — so every edge, greeting-sourced or agent-minted,
is reachable by `unpair`. onbod mints the link and observes the result; it is
not in the redemption path.

**Greeting cadence.** A row is greeted when `prompted_at IS NULL`. The re-arm
lives in `store.InsertOnboarding`, which routd calls once per route MISS: a
chat that messages again after `store.PairingTTL` gets a fresh link, while a
since-routed or silent chat is never greeted again. The dashboard reprompt
button is the operator's bypass for a chat that has gone quiet, and it fully
resets the row (verdict included) so the observer can advance it.

## Tables owned

`onboarding`, `invites`, `invite_redemptions`, `onboarding_gates` (+ its own
`audit_log`).
onbod OWNS these and runs no migration chain: all four are objstore entities
and the driver builds them from the declarations in `store/entities.go`.
`store.OwnerDriver` is the door (`onbod/db.go` `openOwned`): it dials onbod's
own table prefix on the instance's store server, and onbod is the only daemon
that makes those tables.

Writers reach the owned tables through onbod's `/v1/*` admin surface, or write
them directly with the same discipline (host CLI `arizuko invite`/`gate`,
dashd through its own handle).

`invite_redemptions` is the one owned entity with no resreg resource and no
`/v1` path, deliberately. It is not a management entity: it holds one
in-flight authority between redeeming a subgroup invite and naming the
workspace, written by `store.consumeInvite` and deleted by
`handleCreateWorld`. A REST/MCP face would publish a create verb for the
authority that decides which tenant a new folder lands in — the same
authority BUGS `F50` was about. Spec `5/18` step 8.

`auth_users`/`acl`/`groups`/`routes` are NOT onbod's — they are routd's. onbod
holds a second reader on routd's owner (`store.OpenOwner`) for
dashboard rendering, and cross-writes `acl`/`groups`/`routes` during world
creation and invite redemption.

onbod also writes FILES: `container.SetupGroup` creates `groups/<folder>/` and
the per-group `web/pub` and `web/priv` slots, so onbod needs the same tree
routd reads.

## Entry points

- Binary: `onbod/main.go`; admin surface: `onbod/admin.go`; owned-DB
  open: `onbod/db.go`
- Listen: `$ONBOD_LISTEN_ADDR` (default `:8080`)
- Onboarding surface — proxyd routes these `auth: user`, so the caller has
  already signed in and arrives with a transit-verified `X-User-Sub` (authd
  JWKS). Every handler names that caller, and answers 401 without one:
  - `GET /onboard` — world picker (spec 5/18 step 6), username picker (step 8),
    dashboard, or queue position. Carries no `?token=`: redemption is webd's
    `/pair/{token}`.
  - `POST /onboard` — CSRF-protected form actions (create_world, add_route from
    the world picker; delete_route is dispatched but no page renders a form)
  - `GET /invite/{token}` — invite redemption
- Admin surface (bearer-gated, authd JWKS; nil keyset = open):
  - `POST /v1/onboarding` — record unrouted JID (invites:write)
  - `POST /v1/invites`, `GET /v1/invites`, `DELETE /v1/invites/{ref}` (invites:read/write)
  - `GET /v1/gates`, `PUT /v1/gates/{gate}`, `DELETE /v1/gates/{gate}` (gates:read/write)
- `GET /openapi.json` — `onboarding`, `onboarding_gates`, `invites`
- Disable: `ONBOARDING_ENABLED=0` (exits immediately)

## Dependencies

- `auth` (JWT, identity), `chanlib` (env helpers, router client), `container` (SetupGroup), `core`, `store`, `theme`

## Configuration

- `ONBOARDING_ENABLED` — `0` exits immediately
- `ONBOARDING_GREETING` — prepended to auth link message
- `DYNAMODB_URL` + `AWS_*` + `STORE_CA` — the store server onbod dials for its
  own tables and for the cross-read on routd's
- `ONBOD_LISTEN_ADDR` — HTTP listen address (default `:8080`)
- `ONBOARD_POLL_INTERVAL` — poll tick duration (default `10s`)
- `ROUTER_URL` — routd's `/v1/outbound` endpoint (default `http://routd:8080`)
- `AUTHD_URL`, `AUTHD_SERVICE_KEY` — service token for routd's JWT-gated outbound API (required)
- `AUTH_BASE_URL` — public auth base for link generation
- `DATA_DIR` — project root (inherited from core config)

## Health signal

`GET /health` returns 200. Queued users see their position on `/onboard`
(auto-refreshes every 30s).

## `/v1/*` surface

Per `specs/5/17-openapi-mcp.md`:

- `POST /v1/onboarding` — record unrouted JID (status awaiting_message).
  routd's poll loop calls this on route miss. Scope: `invites:write`.
- `/v1/invites` — list/create/revoke invites. routd's `/invite` command
  federates here; the host CLI `arizuko invite` and dashd write onbod's
  tables through their own handle. Scope: `invites:read`, `invites:write`.
- `/v1/gates` — list/upsert/delete onboarding gates. routd's `/gate`
  command federates here; CLI `arizuko gate` + dashd write direct.
  Scope: `gates:read`, `gates:write`.
- No `/v1/users`. `auth_users` and `oauth_identities` are authd's
  (`authd/authstore`), and `dashd /dash/profile/` asks routd for the caller's
  display name rather than reading any identity table.

REST verbs match the platform-api shape (`GET`, `POST`, `PUT`, `DELETE`).

## Token role

onbod uses the same `auth/` library every other daemon does — it is
**not** a special-case auth path. authd is the sole platform-token
signer; onbod never holds a signing key.

- **Transit verifier.** onbod fetches authd's JWKS at boot
  (`auth.FetchKeys` → `KeySet`) and gates its public routes with
  `stripUnsignedGuard(ks)` — keeps the proxyd-stamped `X-User-Sub` (the
  OAuth'd end-user) only when the request proves it transited proxyd via
  a valid ES256 bearer (`auth.ProxydTransit`). The bearer is a transit
  proof ONLY; its `service:proxyd` subject never overwrites the end-user
  `X-User-Sub`. Unproven identity headers are stripped.
- **Admin verifier.** `/v1/*` endpoints check bearer scope
  (`invites:read`, `invites:write`, `gates:read`, `gates:write`) via
  `auth.HasScope` — identical pattern to routd and timed.
- **CSRF protection.** POST /onboard actions require double-submit token
  (cookie `onbod_csrf` + form field `csrf`) to prevent cross-site
  exploitation of the auth proxy cookie.

## Cross-daemon flow

```
new JID → routd POST /v1/onboarding (service:routd bearer) → onbod records awaiting_message
       → onbod poll sends auth link via routd POST /v1/outbound (service:onbod bearer)
       → user clicks link → onbod sets cookies, redirects /auth/login → proxyd OAuth
       → proxyd redirects back to /onboard with X-User-Sub + transit bearer
       → onbod binds JID to user_sub, queues or approves per gates
       → user creates world → container.SetupGroup + acl/groups/routes writes
```

onbod imports `auth/` like every other daemon; verification middleware
is shared, not bespoke.

## Testing

Integration tests set `DATA_DIR` to a temp dir and spin up two stores — onbod's
own and routd's — to match the split topology. `handleCreateWorld`
handles both test and production: when `cfg.core` is nil (test env
without a full config), it calls `core.LoadConfig()` from env vars
rather than panicking. Do not remove this nil-check.

## Files

- `main.go` — config, poll loops (`promptUnprompted`, `admitFromQueue`), public HTTP handlers, CSRF
- `admin.go` — bearer-gated `/v1/*` admin surface
- `db.go` — `openOwned`: the owned-store door and its entity set
- `dash.go` — `/dash/onbod/`, onbod's own operator page
- `invites_resource.go`, `gates_resource.go`, `onboarding_resource.go`,
  `audit_resource.go` — the four resreg resources, REST + MCP from one handler

## Related docs

- `specs/5/32-acl-unified.md` (canonical ACL)
- `specs/5/17-openapi-mcp.md` (full `/v1/*` contract, token model; supersedes 5/5)
- `ARCHITECTURE.md` (Onboarding section)
