# onbod

onbod turns a stranger's chat into a routed chat with the person's consent.
It greets an unrouted chat with a pairing link, admits the paired person
through the gates, and writes the route the person chooses. The pairing
threat model is in [SECURITY.md § Pairing](../SECURITY.md#pairing-consent-is-the-boundary);
the held-chat rules are in [ROUTING.md § Unrouted chats](../ROUTING.md#unrouted-chats-held).

## From an unrouted chat to a routed one

<!-- twin:pairing -->

```
inbound message, no route claims the chat
  │
  ▼
┌──────────────────┐   observe rule matched ──► stored as observed,
│ routd routeMiss  ├──►                         no turn, no greeting
│                  │   else: held under no folder, cursor advances;
│                  │   the operator sees it in /v1/routing/unrouted
│                  │   and may claim it with a chat_jid= rule
└────────┬─────────┘
         │ ONBOARDING_ENABLED, platform allowed,
         │ a Discord guild only on @mention
         ▼ POST onbod /v1/onboarding
┌──────────────────┐
│ onboarding row   │   one per JID, instance-wide
│ awaiting_message │
└────────┬─────────┘
         │ promptUnprompted: mint route_tokens kind=pair,
         │ claim prompted_at, send through routd /v1/outbound
         ▼
chat: greeting + https://<host>/pair/<token>
         │ the person opens it in a browser
         ▼
┌──────────────────┐   proxyd auth=user: no session ──► OAuth,
│ webd /pair/<t>   ├──► back to the same URL
│                  │   GET   confirm page, writes nothing
│                  │   POST  CSRF, RedeemPairing, one batch:
│                  │         acl_membership(child=jid, parent=sub,
│                  │         added_by=pairing) + token delete
└────────┬─────────┘
         │ success page links /onboard
         ▼
┌──────────────────┐   no gate          approved
│ onbod admitJID   ├──►gate matches     queued, chat told
│ observePairings  │   gates, no match  refused, chat told
└────────┬─────────┘
         ▼ browser /onboard
┌──────────────────┐   queued           queue position
│ world choice     ├──►holds no folder  create a world, if invited
│                  │                    or gate-approved
│                  │   1 admin world    route it, via sole_world
│                  │   N admin worlds   picker, via picker
│                  │   0 admin worlds   "no world" page
└────────┬─────────┘
         ▼
routes row: room=<id> → world, added_by=<sub>, added_via
the next message reaches the world; history does not move
```

<!-- /twin -->

A chat needs two things before an agent answers it, and each fails closed on
its own. The **identity axis** is the `acl_membership` edge that pairing
writes: it says who the channel identity is. The **location axis** is a
`routes` row: it says which folder the chat feeds. Routing never reads the
ACL and the ACL never creates a route, so an anonymous sender in an unrouted
chat gets nothing from either.

Two paths write the location, and neither reads the other. Onboarding is the
self-serve path on the identity axis. The operator's claim on the held-chat
view is the location-axis path. A greeted chat that never pairs stays in the
held view until someone claims it.

- **First contact is canned text, never an agent turn.** The greeting is
  `ONBOARDING_GREETING` plus the link. An unknown chat has no folder, so no
  spend cap could bound a turn for it.
- **The link proves the person reads the chat and names no folder.** The
  folder comes later, from the person's own choice on `/onboard`.
- **The observer never writes a route.** `observePairings` advances the
  onboarding row only. The success page at `/pair/<token>` sends the person
  on with a static link to `/onboard` (`pairContinueURL`), because the world
  choice is a browser form. A route appears only when the person loads
  `/onboard`.
- **The onboarding row is one admission queue for the whole instance**,
  keyed on the JID. It names no target and no approver. `approved` is
  instance-wide, and a gate throttles by identity provider; a gate is not a
  world's policy. Any approval that names a location is the route write's
  own authority check below.
- **Admission never refuses a pairing.** `RedeemPairing` writes the edge and
  knows nothing about gates; `admitJID` runs later, on a tick. A refusal
  reaches the chat as a message and the row's status `refused`.

### Who may write which route

`POST /onboard` with `action=add_route` writes a route only when both checks
pass (`handleAddRoute`, `onbod/main.go`):

- The target folder matches a scope of an `allow` row the caller holds
  (`auth.MatchGroups` over `userFolders`).
- The match is `room=<id>`, and `<id>` is the room of a JID paired to the
  caller (`userOwnsMatch`). A caller with the `**` operator row skips this
  check. Every match still passes `matchRe`, which allows no space and no
  wildcard.

The picker offers only folders where the caller holds `admin`
(`adminFolders`). The write check is wider: any `allow` scope on the target
passes it.

`insertRoute` is onbod's only `routes` writer. It stamps `added_by` with the
caller's sub and `added_via` with the act: `picker` when the person chose,
`sole_world` when onbod routed to the only world the person administers. The
provenance is a column on the row and not an audit row, because onbod's
audit log and routd's are different tables.

### Invites

An invite is a bearer link to `/invite/<token>` that grants a place in the
tree. onbod stores `hex(sha256(token))` as the invite's key, never the raw
token. `target_glob` decides the mode (`store.MintInvite`,
`store/invites.go`):

- **No trailing slash is a join.** Redemption writes an `admin` row on that
  folder for the caller.
- **A trailing slash is a subgroup create.** Redemption grants nothing yet.
  It records an `invite_redemptions` row, and the person names the new
  folder under that parent on the username picker. `/` alone makes a
  top-level world.
- **`**` is refused\*\*, because it is not a valid folder.

When the `admin` row write fails, `RestoreInvite` gives the use back, so a
failed redemption does not spend the invite. `handleCreateWorld` creates the
folder with `container.SetupGroup` and writes its rows.

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
  (`mayCreateFirstWorld`) — **only while at least one
  enabled gate exists.** With no gate, `admitJID` approves every paired
  identity, so the gate is what keeps world creation invite-only by default;
  configuring one is the operator's opt-in to self-serve signup.

**onbod writes no `acl_membership` edge.** Identity pairing owns that write,
in routd's transaction,
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
authority BUGS `F50` was about.

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
  - `GET /onboard` — world picker, username picker, dashboard, or queue
    position. Carries no `?token=`: redemption is webd's
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

- [`auth/README.md`](../auth/README.md) — the `acl` row and `acl_membership`
- [`resreg/README.md`](../resreg/README.md) — the `/v1/*` contract
- [`ARCHITECTURE.md` § Onboarding](../ARCHITECTURE.md#onboarding-onbod)
