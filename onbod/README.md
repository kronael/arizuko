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
│ onboarding row   │   one per chat JID, carries inbound sender
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
│                  │         acl_membership(child=sender, parent=sub,
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
  caller (`userOwnsMatch`). A caller whose policy allows admin on `**` skips this
  check. Every match still passes `matchRe`, which allows no space and no
  wildcard.

The picker offers only folders where the caller holds `admin`
(`adminFolders`). The write checks `auth.MatchGroups` over `auth.UserScopes`.
That list includes allow scopes from both policy layers. It includes `**`
only when the policy allows admin on `**`. Other listed scopes do not express
per-target denies, so this write check is wider than an authorization verdict.

`insertRoute` is onbod's only `routes` writer. It stamps `added_by` with the
caller's sub and `added_via` with the act: `picker` when the person chose,
`sole_world` when onbod routed to the only world the person administers. The
provenance is a column on the row and not an audit row, because onbod's
audit log and routd's are different tables.

### A world is a top-level folder

World, agent and session are words for folder shapes, and none is a table. A
world owns nothing of its own: its roster is the `acl` rows on `<world>/**`
and their members, its vhost derives from the name, and its secrets resolve by
folder ancestry. A `worlds` table would be a second path beside each of those.
An agent cannot create a world: a top-level `register_group` from the agent
socket answers "worlds are CLI-only" (`routd/groups_resource.go`). The CLI
creates one, and onbod creates one for a person admitted through an enabled
gate or holding an invite to `/`.

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

onbod checks the issuer's current authority before consuming an invite.
It checks again before an invited folder writes its grants. These checks use
`CheckGrant`; the rule write proves the returned policy version.
The issuers `cli`, `onbod` and `routd` use trusted authority without that check.

When the `admin` row write fails, onbod calls `RestoreInvite` to return the use.
If restoration fails, the invite stays spent and onbod logs the error.
`handleCreateWorld` creates the folder with `container.SetupGroup` and writes
its rows. A refusal after spending a subgroup redemption can leave that
redemption spent without a world.

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

**onbod writes role edges for each world.** Browser pairing writes identity
edges with `added_by='pairing'` in routd's transaction. `unpair` can remove
those identity edges after its authority check. onbod mints the pairing link
and observes the result; it is
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
authority described by BUGS `F50`.

`auth_users` belongs to authd. `acl`, `groups`, and `routes` belong to routd. onbod
holds a second reader on routd's owner (`store.OpenOwner`) for
dashboard rendering, and cross-writes `acl`/`groups`/`routes` during world
creation and invite redemption.

onbod also writes FILES: `container.SetupGroup` creates `groups/<folder>/` and
the per-group `web/pub` and `web/priv` slots, so onbod needs the same tree
routd reads.

## Browser and admin entry points

The binary is [main.go](main.go). proxyd supplies user identity to `/onboard` and `/invite/{token}`.
`GET /onboard` renders world selection, username selection, or queue position.
`POST /onboard` accepts CSRF-protected form actions. A caller without a transit-proven identity receives 401.
`GET /invite/{token}` redeems an invite for the signed-in caller.
The complete resource and operator-action paths appear below under Resource map and recovery.

## Dependencies

- `auth` (JWT, identity), `chanlib` (env helpers, router client), `container` (SetupGroup), `core`, `store`, `theme`

## Configuration

| Variable                                   | Default             | Meaning                                                                                                           |
| ------------------------------------------ | ------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `ONBOARDING_ENABLED`                       | unset               | Literal `0` exits immediately; other values do not disable this binary.                                           |
| `ONBOARDING_GREETING`                      | empty               | Text prepended to the setup link.                                                                                 |
| `ONBOD_LISTEN_ADDR`                        | `:8080`             | HTTP listener; LISTEN_ADDR is not read.                                                                           |
| `ONBOARD_POLL_INTERVAL`                    | `10s`               | Go duration for greeting and pairing scans. Configure a positive duration.                                        |
| `ROUTER_URL`                               | `http://routd:8080` | routd base URL for outbound messages.                                                                             |
| `AUTHD_URL, AUTHD_SERVICE_KEY`             | required            | JWKS and fixed service:onbod bootstrap. AUTHD_SERVICE_NAME is not read.                                           |
| `AUTH_BASE_URL`                            | unset               | Public auth origin used to build links.                                                                           |
| `AUTH_SECRET`                              | unset               | Shared state configuration loaded with the instance.                                                              |
| `WEB_HOST`                                 | unset               | Shared public web origin.                                                                                         |
| `DATA_DIR`                                 | working directory   | Root for group and web-slot creation.                                                                             |
| `ASSISTANT_NAME`                           | `arizuko`           | Instance display name from core config.                                                                           |
| `SECRETS_KEY`                              | unset               | Keyring loaded by core config for group setup context.                                                            |
| `DYNAMODB_URL`                             | required            | Store endpoint.                                                                                                   |
| `AWS_REGION`                               | `us-east-1`         | Store signing region.                                                                                             |
| `AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY` | required            | Daemon store identity.                                                                                            |
| `STORE_CA`                                 | unset               | Base64 PEM endpoint certificate; unset uses system trust.                                                         |
| `ARIZUKO_INSTANCE`                         | unset               | Audit and observability instance label.                                                                           |
| `LOG_LEVEL`                                | `info`              | JSON stderr verbosity.                                                                                            |
| `OTEL_EXPORTER_OTLP_ENDPOINT`              | unset               | Optional OTLP logs through obs.                                                                                   |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`       | unset               | Optional OTLP trace export; independent of the log endpoint.                                                      |
| `MAX_CONCURRENT_CONTAINERS`                | absent              | Shared config rejects this variable whenever it is present; configure MAX_CONCURRENT_AGENTS for execution limits. |
| `METRICS_ENABLED`                          | `false`             | Literal true mounts public GET /metrics.                                                                          |

[core/config.go](../core/config.go) also parses the shared instance fields; OAuth login itself belongs to authd.

## Health signal

`GET /health` returns 200. Queued users see their position on `/onboard`
(auto-refreshes every 30s).

## Token role

onbod uses the same `auth/` library every other daemon does — it is
**not** a special-case auth path. authd is the sole platform-token
signer; onbod never holds a signing key.

- **Transit verifier.** onbod constructs authd's lazy JWKS verifier at boot
  (`auth.FetchKeys` → `KeySet`) and gates its public routes with
  `stripUnsignedGuard(ks)` — keeps the proxyd-stamped `X-User-Sub` (the
  OAuth'd end-user) only when the request proves it transited proxyd via
  a valid ES256 bearer (`auth.ProxydTransit`). The bearer is a transit
  proof ONLY; its `service:proxyd` subject never overwrites the end-user
  `X-User-Sub`. Unproven identity headers are stripped.
- **Admin verifier.** `/v1/*` endpoints check bearer scope
  (`invites:read`, `invites:write`, `gates:read`, `gates:write`) via
  `auth.HasScope` — identical pattern to routd.
- **CSRF protection.** POST /onboard actions require double-submit token
  (cookie `onbod_csrf` + form field `csrf`) to prevent cross-site
  exploitation of the auth proxy cookie.

## Testing

Integration tests set `DATA_DIR` to a temp dir and start two stores — onbod's
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
- [`ARCHITECTURE.md` § Data ownership](../ARCHITECTURE.md#data-ownership) — the routd tables onbod opens

## Resource map and recovery

| Resource           | REST endpoints                                                                                                                         | MCP surface                                                                                                                                       |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `onboarding`       | `POST`, `GET /v1/onboarding`; `POST /v1/onboarding/{jid}/approve`, `POST /v1/onboarding/{jid}/reprompt`, `DELETE /v1/onboarding/{jid}` | Catalog names `onboarding.create`, `onboarding.list`, `onboarding.approve`, `onboarding.reprompt`, `onboarding.delete`; no agent socket in onbod. |
| `onboarding_gates` | `GET /v1/gates`; `PUT`, `DELETE /v1/gates/{gate}`                                                                                      | No derived tools; routd's hand-authored gate tools federate here.                                                                                 |
| `invites`          | `POST`, `GET /v1/invites`; `DELETE /v1/invites/{ref}`                                                                                  | No derived tools; routd's hand-authored invite tools federate here.                                                                               |
| `audit`            | `GET /v1/audit`                                                                                                                        | No onbod agent socket.                                                                                                                            |

The public browser routes are `GET`, `POST /onboard` and `GET /invite/{token}`.
The operator page uses `GET /dash/onbod/` and `POST /dash/onbod/approve/{jid}`, `/dash/onbod/deny/{jid}`, `/dash/onbod/reprompt/{jid}`.
`GET /health`, `GET /openapi.json`, and optional `GET /metrics` are public.
No MCP or IPC listener runs here; routd owns the agent tool socket.

`StoreReach["onbod"]` reaches `onbod` and `routd`.
Owned entities hold admissions (`onboarding`), limits (`onboarding_gates`), invite hashes and use counts (`invites`),
unspent subgroup authority (`invite_redemptions`), version markers (`resreg_scopes`), and audit events (`audit_log`).
The cross-owner handle reads or writes routd's groups, routes, grants, membership, profiles, and pairing tokens.
Disk writes create `DATA_DIR/groups/<folder>`, `DATA_DIR/web/pub/<folder>`, and `DATA_DIR/web/priv/<folder>`.
No local onboarding database or delivery spool exists.

Startup runs greeting, pairing observation, and queue admission once.
The configured poll then runs greeting and pairing scans every 10 s by default.
Queue admission runs when accumulated whole poll seconds reach 60; it is not a separate exact 60 s ticker.
The outbound HTTP client has a 10 s timeout. SIGTERM closes HTTP immediately.
A failed outbound greeting keeps the row eligible for a later poll; store errors log and the next pass tries again.
Store opening or required auth configuration failure exits. `/health` still returns 200 during runtime dependency faults.
Grep `open onbod's store`, `open routd's store`, `onbod: unproven identity stripped`, and `http server error`.

A re-greet cooldown limits rate, not total lifetime greetings ([BUGS F41](../BUGS.md)).
Cross-owner writes cannot share one store transaction; invite-use restoration compensates a failed routd write.
Tests cover invite limits, queue admission, pairing, and handler gates; live OAuth and outbound platform delivery need integration checks.

Sources: [boot, config, loops](main.go), [owned store](db.go), [resource contracts](../resreg/resources/onboarding.go),
[entity ownership](../store/entities.go), [owner reach](../store/owner_reach.go).

JWKS keys load lazily during verification through auth.FetchKeys; there is no fixed background JWKS ticker.
Service tokens refresh on demand within 60 s of expiry, with a 10 s exchange timeout ([auth/service.go](../auth/service.go)).
