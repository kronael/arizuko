---
status: partial
depends: [5/13-ext-mcp, 5/17-openapi-mcp, specs/5/32-acl-unified]
moved_from: specs/9/index.md §1 (was "phase 8 action 1"; pulled to phase 5)
---

> **Status (2026-07-07).** Agent-MCP faces: all SEVEN agent-facing cold-tier
> resources — `web_routes`, `routes`, `network_rules`, `scheduled_tasks`, `acl`,
> `route_tokens`, `groups` — ride one `resreg.Resource` via the injected Gate.
> `secrets` is REST-only, write-only (no agent face). REST second faces folded
> onto the shared handler: `web_routes`, `acl`, `routes`, `scheduled_tasks`,
> `secrets`, plus onbod's `/v1/invites` (`154cd17f`) and `/v1/gates`
> (`4bd09532`). Containment for the tier-structural handlers (`routes`,
> `scheduled_tasks`) is a routd-internal per-face `containFn` (agent → tier
> `AuthorizeStructural`, REST → `ownsFolder`), which closed a live cross-tenant
> task leak (`0d25b687`); the same empty-folder key later closed a `web_routes`
> tier-0 delete + a `tasks` get twin (`ad44b081`). `network_rules` is agent-only
> (no REST twin) — nothing to fold. OpenAPI emits each advertised resource's real
> mounted `Endpoints` (`7c14efd6`); the dashd tool-browser renders the facade
> tools (`df9ebad3` + `d5023c60`). **2026-07-29:** `route_tokens`' REST face is
> now folded onto the shared handler (`tokens_http.go mountRouteTokens` +
> `routeTokensRESTGate`) — the bespoke `handleToken*` bodies and the
> `RouteTokenResponse`/204 wire shapes are retired; the REST face returns the
> unified handler shape (`{token,jid,url}` / `{tokens:[…]}` / `{deleted}`) and the
> REST-only `resolve` (no MCP twin) stays hand-rolled. **2026-07-30:** `groups` REST twin
> SHIPPED as read-only (`routd/groups_http.go` `mountGroups`): `GET /v1/groups`
> rides the shared handler, scoped to the caller's JWT subtree (closes the
> rest_listall leak); the agent MCP face stays unscoped. Only LIST is mounted —
> operator group CREATE stays dashd's FS-managed `SetupGroup` by write-discipline,
> so routd opens no second bare create door (the decision recorded in `BUGS.md`,
> resolved as option a). **Every agent-facing cold-tier resource now wears both
> faces** (or the intended read-only/create-elsewhere split). Then the one-owner +
> federation phase retires `messages.db`.
> **2026-07-27:** `mcp_connectors` was floated for the adoption list but CUT
> (2026-07-29): connectors + REST ext-providers ALREADY load from
> `<datadir>/connectors.toml` — a resreg resource would add a second
> source-of-truth (the reconciler-drift trap). Per-group `MCP.json` dropped;
> connectors are global operator-defined resources, access via grants only.

# specs/5/16 — MCP+REST unification (finish the adoption)

[`5/17-openapi-mcp`](17-openapi-mcp.md) is the **mechanism** — one
`resreg.Resource` per `(resource, action)` wearing two faces: the agent's
MCP tools (derived by `deriveMCPTools`) and the human's annotated REST
`/openapi.json`. This spec is the **adoption program**: migrate every
resource onto that mechanism and delete the parallel hand-rolled surfaces.
Orthogonal to `5/17` — mechanism vs rollout.

## The target

One cold-tier handler, **two faces from one `resreg.Resource`**, one owner:

```
       one in-process resreg.Resource (Handler + Endpoints + MCPDoc)
                    │                                  │
       deriveMCPTools + injected Gate         openapi.go (x-mcp-when)
                    ▼                                  ▼
      MCP tools (agent, tier-gated)     REST /v1/<res> + annotated doc
                    │                                  │
                    ▼                                  ▼
                the agent                    dashd HTML + external CLIs
                                             (call the REST face — no
                                              hand-rolled CRUD)
```

- **One handler, two faces.** One in-process `resreg.Resource` per
  cold-tier management resource; authorization is an **injected `Gate`**,
  not resreg policy — operator REST defaults to `auth.Authorize`, the
  agent socket injects its `mcp:`+tier `db.Authorize`. The agent's MCP
  tools are `deriveMCPTools`'d from its `Endpoints`+`MCPDoc`; the REST doc
  is emitted from the same `MCPDoc` — never a second hand-written tool
  list.
- **Hot-tier stays MCP-only** in `ipc/ipc.go` (`reply`/`send`/`inspect_*`) —
  no REST resource to derive from; hand-authored.
- **One owner + federation**: each resource's table lives in exactly one
  daemon's DB; cross-daemon reads call the owner's face over HTTP — the owner
  resolves by compose service naming (`<DAEMON>_URL`), never a second
  `store.Open`. This retires `messages.db` as a shared 8th DB.

(Current state is the **Status** blockquote at the top — one source, no drift.
What follows is the design rationale: why the agent socket needs work resreg
doesn't do, and the `Gate` seam that resolved it.)

## Why the agent socket needs work resreg doesn't yet do

`proxyd_routes` proves the two-face mechanism ONLY on the OPERATOR socket:
its MCP face lives on **webd's operator socket**, callers carry a `**` ACL
row, and it is a GLOBAL forwarder with no folder-containment. The **agent
socket** (`ipc.buildMCPServer`, per-folder in-container agent) authorizes
on a different axis than `resreg.invoke` models today:

- **Tier, not scope.** The agent path keys `mcp:<tool>` with
  `AuthorizeOpts{Folder,WorldFolder,Tier}` → the tier-default-grants
  fallback (`auth/authorize.go`, `grants/grants.go`). `resreg.invoke`
  hardcodes `auth.Authorize("<name>:<action>", …)` with EMPTY opts → no
  fallback → it DENIES every folder agent (current path allows
  `mcp:set_web_route` for a tier-0 folder; a naive resreg path denies
  `web_routes:create`).
- **Visibility is a separate firewall.** `granted()`/`MatchingRules`
  (`ipc/ipc.go`) gate which tools a tier even SEES in `tools/list`. An
  unconditional `resreg.MCPTools` `AddTool` would WIDEN visibility.

## Resolution — the injected `Gate` seam (not "bake agent policy into resreg")

resreg does NOT re-implement `granted()` or tier logic. Instead resreg
gains a **`Gate` seam** on the `Resource` and stays policy-free:

- **resreg** owns the handler, tx, audit, arg-derivation, and tool-spec
  generation, and CALLS an injected `Gate`. The `Gate` DEFAULTS to today's
  operator `auth.Authorize` — so proxyd/webd and every REST mount are
  unchanged.
- **routd** mounts the agent tools and OVERRIDES the `Gate` with the
  proven agent closure: `db.Authorize(sub, folder, "mcp:"+tool, params)`
  (`routd/sibling_db.go`) — the tier-default-grants path intact.
- **In-process, not a second server.** The agent MCP tool is a GENERATED
  thin facade over the SAME in-process handler (`Store` non-nil), so the
  one-audit-row-in-the-mutation's-tx invariant holds. It is neither an
  HTTP forward nor a second authz server.
- **Visibility stays in the socket filter.** `resreg.MCPTools` does not
  decide tier visibility; agent-tool registration stays driven by the
  socket's `MatchingRules` filter (or a `visible(name)` predicate).

This supersedes the earlier open A/B choice (A: re-implement the
tier/`granted()` decision inside `resreg.invoke`; B: share only the
handler, keep agent MCP fully hand-rolled). The `Gate` is the third path —
resreg stays policy-free (A's flaw avoided) while the agent tool bodies
still collapse onto the shared handler (B's duplication removed). Enabler
already shipped: the `MCPNames` override that keeps flat agent tool names
(`add_route`, `set_web_route`) across the migration (`443dc4d3`).

## The deliverable: MCP management of the whole platform, written once

The goal is **agent-first platform management** — every cold-tier management
resource (`routes`, `acl`, `groups`, `secrets`, `scheduled_tasks`,
`network_rules`, `web_routes`, `route_tokens`, `onboarding_gates`,
`proxyd_routes`) reachable via **MCP** (agent, tier-gated) AND **REST**
(human, OAuth-gated), one in-process handler. Both-faces is the **default,
not a universal** — security-driven exceptions stand and are intended:
`secrets` is REST-only (write-only, no agent face — the encrypted value must
never ride the agent surface); `network_rules` is agent-MCP-only (no REST
twin). That's `5/17`'s principle;
this spec finishes the coverage and collapses the bespoke surfaces onto it.

**Written once, not twice** — one `resreg.Resource` is authored; the agent's
MCP tools are `deriveMCPTools`'d from its endpoints and the REST doc emitted
from the same `MCPDoc`. No hand-authored `MCPTools` list per resource;
shipping 5/17 is the mechanism this spec rides.

**Two surfaces stay distinct — don't merge them:**

- **Cold-tier management** (`/v1/*` resources) — REST-authored, MCP-derived.
  `ipc/ipc.go`'s hand-rolled management tools (`list_routes`/`add_route`/…)
  and `dashd`'s hand-rolled CRUD collapse onto this one handler per resource.
- **Hot-tier agent actions** (`reply`, `send`, `engage`, `inspect_*`) —
  **MCP-only by design**; no REST resource to derive from. They stay
  hand-authored in `ipc/ipc.go`. So ipc.go doesn't disappear — it loses its
  management tools, keeps the hot-tier tools + the unix-socket transport.

The orthogonal sibling concern is `5/8` — the **same cold-tier resources as
declarative YAML manifests** you `export`/`apply` (config-as-data). 5/16 is
the runtime REST+MCP surface; 5/8 is the manifest transport. Same tables,
two orthogonal fronts — the row-level plumbing underneath both is not a
concept worth naming.

## Two resources share the label `routes` (don't conflate them)

- **message-routing** `routes` (routd's `routes` table: match→target folder):
  COLLAPSED onto one `routesResource` handler (`routd/routes_resource.go`)
  serving both faces — agent MCP (`add_route`/`set_routes`/`list_routes`) and
  operator REST `/v1/routes` (`routd/routes_http.go` via `resreg.RegisterREST`).
  The design-call that held it back — `{id}` REST addressing vs
  `(seq,match,target)` PK + the seq-0 self-default guard — resolved in the
  shared handler; containment is the per-face `containFn` (agent → tier
  `AuthorizeStructural`, REST → `ownsFolder`).
- **HTTP-proxy** `proxyd_routes` (proxyd's table): proxyd resreg REST
  `/v1/proxyd_routes` + `webd/routes_mcp.go` MCP forwarder. **Already full
  dual-dispatch — the exemplar to replicate, not a surface to collapse.**

## Migration recipe (per resource)

Every cold-tier resource migrates the same shape; each step ships and
reverts independently:

1. **Extract the shared in-process handler** — one handler holding the
   resource's logic, tx, and arg-derivation, folding in the bespoke
   semantics the hand-rolled agent tool carried: `set_routes`' seq-0
   self-default guard, `del_web_route`'s tier-0 delete widening, a
   resource's self-slot / path-claim rule. This handler is the single
   renderer both faces sink to.
2. **Mount REST** via `resreg.RegisterREST` with the DEFAULT `Gate`
   (operator `auth.Authorize`) + `x-mcp-when` OpenAPI annotation; `dashd`'s
   admin page calls this face instead of its direct-DB CRUD.
3. **Keep the agent tool's socket registration** — it stays registered
   through `buildMCPServer` so the tier visibility filter
   (`MatchingRules`) and the `mcp:`+tier authz (`db.Authorize`, via the
   injected `Gate`) stay intact; only its BODY changes to delegate into
   the shared handler. Nothing of the agent's proven auth path moves into
   resreg.
4. **Reconcile the audit shape** — the agent path's `emitSys` /
   `LogIPCAudit` and resreg's `audit.EmitInTx` must write the SAME row for
   the same action, so `grep 'action=<verb>'` returns work regardless of
   surface. Drift here re-splits the surfaces the migration just merged.
5. **Delete only the per-tool BODY.** The shared closures — `registerRaw`,
   `granted`, `authorizeCall`, `authzStructural`, `emitSys` — and every
   hot-tier tool (`reply`/`send`/`inspect_*`) STAY; `ipc.go` keeps the
   unix-socket transport plus the agent's auth/visibility scaffolding.

**Order** — tractable first (both faces already mirror one writer, parity
provable): `web_routes` (where the `Gate` blocker surfaced),
`network_rules` (MCP-only → add a REST face), `onboarding_gates` (near-pure
CRUD on onbod), `scheduled_tasks` (cron parsing in the handler), `secrets`
(write-only: create + delete, no list/get). Then the design-call resources
that need an addressing/scope decision first: `routes` (`{id}` REST key vs
`(seq,match,target)` PK + seq-0 guard), `acl` (`**`→membership branch,
body-DELETE — take the stricter MCP scope-containment, closing a REST
cross-folder hole), `groups` + `acl_membership` (cross-daemon writers,
spawn side-effects — highest risk).

**One owner + federation** (final) — fold each resource's table to its
owner daemon, repoint non-owner reads to the owner's face, and retire
`messages.db` + its `store/migrations` twin once no reader remains. The
resource model this phase realizes is specified next.

## One owner + federation

This section is the **canonical model** for what a cold-tier resource is;
[`5/8`](8-yaml-manifests.md) (YAML transport), [`5/17`](17-openapi-mcp.md)
(two-face mechanism), and [`5/18`](18-onboarding-model.md) (onboarding flow)
reference it rather than restating it. Rewritten 2026-08-02 after adversarial
review rejected the prior version for four defects, fixed below: an
unbuildable "no handler body" claim, a false "compile error" guarantee, an
FK-completeness gap, and an owner-DB map that named a 0-byte file nothing
opens. It also resolves the open auth-ownership question `auth.Authorize`'s
Store dependency raises, using evidence gathered against live krons (BUGS.md
`A1`/`Y1`).

**One owner DB, one single-source SHAPE declaration.** Each cold-tier
resource is owned by exactly one DB, and its addressable shape — the tagged
`RowType`, `PKFields`, and real `Endpoints` — is declared **once** in
`resreg/resources/<name>.go`. That declaration has **no `Handler`**: the
handler is resource-specific business logic (the `routes` seq-0 self-default
guard, `scheduled_tasks`' cron parsing, a tx-scoped write) that inherently
needs a live `*store.Store` and therefore can only exist where a daemon
mounts the resource — it is NOT single-sourceable, and the prior wording's
"adds ONLY the injected `Gate` + per-face `containFn`" was wrong on this
point (BUGS.md's "Resource identity" sweep already has the correct shape:
the mounted handler adds `Store` + `Handler` + `Gate`). What single-sources
is the SHAPE, consumed by:

- the standalone `arizuko apply`/`export`/`plan` CLI, which runs **without
  routd** — an FS-mounted operator tool opening the owner DBs directly (the
  split write-discipline already puts an FS-mounted CLI on the owner DBs); and
- the owning daemon's mounted handler (routd for most, onbod for
  `onboarding_gates`/`invites`, proxyd for `proxyd_routes`), which imports
  `Name`/`Table`/`RowType`/`PKFields`/`Endpoints` from the registry and adds
  `Store` + the resource's `Handler` + the injected `Gate` + per-face
  `containFn`.

This closes the **shape** half of the two-declaration drift (BUGS.md
"Resource identity"): today `routd/*_resource.go` restates `Name`/`Table`
instead of importing them from `resreg/resources/*.go`, so OpenAPI's
`Endpoints` and the mounted handler's served routes CAN diverge silently.
Single-sourcing the shape does not make that divergence a **compile error**
— `Action` is a plain `string` (`resreg/resreg.go:55`) and every handler
`switch`es on it, so a `RowType`+`Endpoints` pair with no matching `case` in
its `Handler` compiles fine and 404s/500s at request time. The realistic bar
is a **shared-identity test**, already proposed in BUGS.md as the interim
guard: `mounted.Name == registry.Name` (and, once `Handler` importing the
registry's `Endpoints` directly instead of a hand-copied switch list becomes
the norm, a per-action coverage assertion) per resource, run in `make test`.
Nothing here claims compile-time enforcement; that claim is dropped.

### What "owner" enforces today, and what makes it real

**Today, "owner" is a convention, not a boundary.** `compose/compose.go:811`
(`writeSvc`) binds `<dataDir>:/srv/app/home` **read-write, whole-tree, with
no per-daemon branch** into every non-custom service; every daemon also runs
`user: '1000:1000'` (`compose.go:800`), so file permissions can't separate
them either — same uid, same directory, every DB in it. Verified live: any
daemon can `sql.Open` any `.db` file in `store/`. CLAUDE.md's "non-mounted
daemons (slakd, timed) write via the owner's HTTP API" describes a boundary
that does not exist as an enforcement mechanism — it happens to hold for
`timed`/`davd`/`teled` (they open no DB) and is violated today by `slakd`
(`slakd/main.go:44` opens `routd.db` directly for pane reads, despite the
same CLAUDE.md line naming it an HTTP writer).

**The mechanism that makes it real: per-daemon named mounts, not a blanket
bind.** Evaluated codex's proposal (per-owner `store/<owner>/` subdirectories,
each bound into only its owning daemon) against this codebase's actual
cross-daemon reads and adopted a **corrected form** — codex's plain "owner
mounts its subdirectory, nobody else gets `store/` at all" is too strict: it
would break `proxyd`'s FS-mounted `routd.db` access for `proxyd_routes`
routing (`Y1`, already decided: proxyd is FS-mounted and split
write-discipline lets it write that table directly) and `onbod`'s documented
`routd.db` cross-read (`onbod/main.go:101-108`). The corrected mechanism is
an **explicit per-daemon volume list in `writeSvc`**, derived from what each
daemon's code actually opens (verified by grep, not guessed), not a rule
that infers access from ownership alone:

1. Restructure `store/` into per-owner subdirectories: `store/authd/`,
   `store/routd/`, `store/onbod/`, `store/runed/` (each holding its
   `<name>.db` + `-wal`/`-shm` siblings — SQLite's WAL sidecars must move
   and mount with their DB, never split across binds). `store/messages.db`
   stays flat until retired (it has no "owner" — it's the frozen pre-split
   file).
2. `svcDef` gains a `storeMounts []string` field (owner names, e.g.
   `["routd"]`, `["onbod", "routd"]`) that `writeSvc` expands into named
   binds — `<dataDir>/store/<owner>:<containerDataMount>/store/<owner>:rw|ro`
   — **replacing** the blanket `<dataDir>:<containerDataMount>` bind for
   every daemon that goes through `writeSvc`. `groups/`, `ipc/`, `web/`
   become their own explicit per-daemon binds the same way (mirroring what
   `davdService`/`vitedService` already do by hand — `compose.go:1013-1060`
   mount only `groups:/data` and `web:/web` respectively; they are the
   existing proof this pattern works, just not generalized).
3. Verified per-daemon mount set (grep-traced, this pass):

   | Daemon           | `store/` access                                                                                                                                     | `groups/`                                                           | `ipc/`                                                              | docker.sock    |
   | ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- | ------------------------------------------------------------------- | -------------- |
   | `authd`          | `authd/` rw only                                                                                                                                    | —                                                                   | —                                                                   | —              |
   | `routd`          | `routd/` rw only                                                                                                                                    | rw (persona/skill reads at dispatch)                                | rw (hosts the agent MCP socket, `ipc/ipc.go:319`)                   | —              |
   | `runed`          | `runed/` rw only                                                                                                                                    | rw (`SetupGroup`, persona/`CLAUDE.md` reads, `container/runner.go`) | rw (`os.MkdirAll` + bind-mount prep, `container/runner.go:555-561`) | yes (existing) |
   | `onbod`          | `onbod/` rw + `routd/` rw (cross-write: `user_profiles.username`, `onbod/main.go:784`; cross-read: groups/routes/acl for onboarding decisions)      | rw (`SetupGroup`)                                                   | —                                                                   | —              |
   | `proxyd`         | `routd/` rw (`route_tokens`, `proxyd_routes`; no auth-table access needed after the fix below)                                                      | —                                                                   | —                                                                   | —              |
   | `dashd`          | `messages.db` (legacy, ro) + `routd/` + `onbod/` + `runed/` (broad by design — the operator console; scope to what it reads, not the whole tree)    | rw (`SetupGroup`)                                                   | —                                                                   | —              |
   | `webd`           | `routd/` rw (route resolution, history, audit sink) — after dropping its own dead `messages.db` open, see below                                     | —                                                                   | —                                                                   | —              |
   | `slakd`          | `routd/` ro (pane reads — CLAUDE.md's write-discipline line is wrong for this daemon today; fix the doc or fix the code, tracked, not decided here) | —                                                                   | —                                                                   | —              |
   | `timed`, `teled` | none                                                                                                                                                | —                                                                   | —                                                                   | —              |

   The remaining channel adapters (`bskyd`/`discd`/`emaid`/`kokoro`/`linkd`/`mastd`/`reditd`/`ttsd`/`twitd`/`whapd`) are not yet grep-audited — step 6 below ("Ordered, independently-shippable steps") covers them before their mounts narrow. Emitting a narrower mount for a daemon before its access is traced is how you break a live daemon; don't skip the audit.

4. What breaks: any code relying on a daemon opening a DB it isn't listed for now fails at container boot (`sql.Open` on a path that doesn't exist under its mount) instead of silently succeeding — this is the point. `dashd`'s runed-view banner (`dashd/runed_page.go:38`, "runed store unavailable") already handles a missing `runed.db` gracefully, so a partial mount there degrades, doesn't crash.

### Owner-DB map

Each owner daemon owns + migrates its own DB and the resources below.
`config_meta` is **per-owner-DB**, added via that daemon's own migrations —
**decided** (`Y1`, user, 2026-08-02): exports are per subsystem, so there is
one file/one DB/one transaction/one version per export, and the
cross-owner partial-apply recovery machinery the prior draft specified is
**deleted, not built** — it solved a problem (one manifest spanning several
DBs) that per-subsystem export no longer has.

| Owner DB   | Owned tables                                                                                                                                                                                                                      |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `routd.db` | `groups`, `acl`, `acl_membership`, `routes`, `web_routes`, `secrets`, `network_rules`, `route_tokens`, `scheduled_tasks`, `task_run_logs`, `user_profiles` (renamed from `auth_users`, see below), `proxyd_routes`, `config_meta` |
| `onbod.db` | `onboarding_gates`, `invites`, `config_meta`                                                                                                                                                                                      |

`proxyd.db` is **not an owner** — deleted (0 bytes, referenced by no Go
code; `Y1` decided this). `proxyd_routes` is migrated by
`routd/migrations/0015-auth-sessions-proxyd-routes.sql` → owner is
`routd.db` by this spec's own rule ("owned by the DB that migrates its
table") — proxyd _serves_ it over REST/MCP and writes it directly
(FS-mounted, split write-discipline), it does not _own_ it. Same shape as
`timed` reading `scheduled_tasks`: **subsystem** (who serves the face) and
**owner DB** (who migrates the table) are different axes; `resreg.Resource`
needs a typed `Owner` field (`OwnerRoutd`/`OwnerOnbod` — logical owner, not a
filesystem path or the serving daemon) to carry this, which it does not have
today (`resreg/resreg.go:155-195` has no `Owner` field — `Y1`'s
recommendation, not yet built).

`authd`'s `auth.db` is a **separate owner**, outside this table: it owns
`identities`, `oauth_identities`, `refresh_tokens`, `signing_keys`,
`identity_claims`, `auth_users` — none of these are resreg-managed cold-tier
resources (no agent/REST CRUD face), so they don't belong in the resource
owner-DB map; they're identity infrastructure. `authd`'s `auth_users`
(`user_id TEXT PRIMARY KEY`, anchors `oauth_identities` FK,
`authd/migrations/0001-authd-schema.sql:22-26`) and `routd.db`'s former
`auth_users` (`sub TEXT UNIQUE`, carries `linked_to_sub`/cost caps,
`routd/migrations/0011-cost-log-user-sub.sql:16-25`) are **different tables
that happened to share a name** — different PK, different purpose, both
live, both empty on krons at time of writing (verify on every instance
before the rename below). The fix is renaming routd's copy, not merging the
schemas; see the auth decision.

### The auth decision — routd stays the policy decision point

`auth.Authorize(s *store.Store, caller, action, scope, params)`
(`auth/authorize.go:25`) is the sole runtime ACL evaluator and it needs a
LOCAL `*store.Store` — `resreg`'s `defaultGate` (`resreg/resreg.go:529`)
calls it against `r.Store`, the mounting daemon's own handle. A DB holding
only routing tables (had `proxyd.db` ever been real) cannot authorize
anything with this function; something must either hold `acl`/
`acl_membership` locally or get a decision over the wire. Two ways to close
that:

**A — move `acl`/`acl_membership`/`auth_users` to `auth.db` under authd**
(BUGS.md `A1`'s proposal: "the daemon whose entire job is auth does not hold
the authorization rows"). Rejected. `acl`/`acl_membership` are queried
in-process, per-call, by the **two highest-QPS authorization paths in the
system**: `resreg`'s default `Gate` for every routd-mounted REST mutation,
and `routd/sibling_db.go:183 (d *DB) Authorize` — the agent MCP `Gate`
(`db.Authorize(sub, folder, "mcp:"+tool, params)`) called on **every agent
tool invocation, every turn**. Moving the rows to authd forces both onto a
cross-daemon HTTP round-trip, on routd's busiest paths, to serve a table
that today has exactly one under-evidenced beneficiary (proxyd — see below).
`acl`'s references are polymorphic strings (no FK ties it to `groups`,
verified: `routd/migrations/0007-acl.sql` has no `REFERENCES`) so it is
physically _free_ to move; the objection is latency/availability, not
schema coupling.

**B — routd stays the policy decision point (PDP); `acl`/`acl_membership`
stay in `routd.db`.** **Chosen.** The task framing also offered "a full
`(principal, action, scope, claims, params)` endpoint" as B's completion,
noting `handleUserScopes` (`routd/server.go:520`) "loses denies, actions,
predicates and params." Traced every live non-owner authorization
consumer to check whether that endpoint is actually needed:

- **`onbod`'s REST gates** (`onbod/gates_resource.go:84`,
  `invites_resource.go:56`) read `x.Caller.Claims["scopes"]` — a string
  already present on the VERIFIED JWT, no DB call, no HTTP call. Zero
  dependency on routd.db already.
- **`proxyd`'s `/priv/*` + WebDAV gate** (`auth.MatchGroups(gs, folder)`,
  `proxyd/main.go:601,760`) needs a coarse folder list, sourced from
  `groupsForSub` → `s.stRoutd.UserScopes(sub)` — a **direct `routd.db`
  read**, the one genuinely live dependency. But `UserScopes` is exactly
  `handleUserScopes`'s output, and authd **already snapshots it into every
  minted JWT**: `issueSession`/`Refresh` call `o.snapshot` (the same grants
  fetch) and mint `TokenClaims{Scope: scope}` (`authd/oauth.go:269-281`), so
  `auth.Subject.Scope []string` (`auth/es256.go:123`) carries the same list
  `groupsForSub` re-fetches from the DB **after having just verified the
  token that already contains it**. `proxyd/main.go:847,890` (`tryAuth`'s
  ES256 branch, `tryRefreshViaAuthd`) call `s.groupsForSub(sub.Sub)` instead
  of reading `sub.Scope` directly — a redundant DB round-trip on data
  already in hand.

No live caller needs per-action/per-predicate evaluation it can't already
get from its own verified token. **Do not build the fine-grained
`/v1/authorize` endpoint now** — it has no consumer; building it is
speculative work against a caller that doesn't exist. Build it only when one
does.

**Concrete fixes this decision produces** (each independently shippable,
detailed in the migration path below):

1. `proxyd`: read `sub.Scope` instead of calling `groupsForSub`/`UserScopes`.
   **Trade-off requiring explicit sign-off before shipping**: grant
   revocation currently takes effect on proxyd's very next request (live DB
   read); after this change it takes effect on the caller's next token
   refresh — bounded by `accessTTL = 15 * time.Minute` (`authd/main.go:25`).
   This is a real security-relevant latency, not a refactor; record and get
   sign-off, don't ship it silently.
2. `auth_sessions` (`routd.db`) has **no live writer** —
   `store.CreateAuthSession` (`store/auth.go:123`) is called from nowhere
   except tests and a one-time split-cutover copy helper
   (`routd/db.go:80-110`). `proxyd`'s cookie branch that reads it
   (`main.go:876-890`) is dead code: authd's OAuth flow writes its own
   `refresh_tokens` row in `auth.db`, never a `routd.db` `auth_sessions` row,
   so `AuthSession(hash)` always misses and `requireAuth` falls through to
   `tryRefreshViaAuthd` (the live path) anyway. Delete the table + the dead
   Go code (verify zero rows on every instance first — destructive).
3. Rename `routd.db`'s `auth_users` → `user_profiles` (schema unchanged) —
   removes the same-name-different-schema collision fact #3 flags, without
   touching `authd`'s `auth_users` (a different table, different owner,
   untouched).
4. `webd` has the SAME dead-`messages.db`-handle pattern proxyd just shed
   (`9ff70eef7`, today): `webd/main.go:60-65` opens `store.Open(cfg.storeDir)`
   into `st`, defers its close, and never uses it again — its own comment at
   `webd/main.go:78` says so. Same fix, same shape, its own commit.

### FK co-location invariant

SQLite foreign keys cannot cross DB files. **Four** cross-table FKs are
declared (the prior count of three omitted authd's), all `ON DELETE
CASCADE`:

- `web_routes.folder → groups.folder` (`routd/migrations/0001-initial-schema.sql:137`)
- `route_tokens.owner_folder → groups.folder` (`routd/migrations/0001-initial-schema.sql:144`)
- `task_run_logs.task_id → scheduled_tasks.id` (`routd/migrations/0009-tasks.sql:25`)
- `oauth_identities.user_id → auth_users.user_id` (`authd/migrations/0001-authd-schema.sql:33`, inside `auth.db` — a second, independent owner DB, not `routd.db`)

The first three sit inside `routd.db` — parent and child of every
`routd.db`-declared FK share that one owner DB, and the fourth sits entirely
inside `auth.db` the same way. This is a **rule that constrains any future
owner assignment**: FK-linked resources MUST share an owner DB. An
assignment that would split an FK across DB files is invalid — either drop
the FK (the reference becomes a string, `5/8`'s FK-posture section) or keep
the resources co-located. `acl`/`acl_membership` have no FK either way
(confirmed above) — they stay in `routd.db` for the latency reason in "The
auth decision," not because anything forces them there.

### Migration path per instance

Applies identically to krons, sloth, marinade (the three live instances) —
run each numbered step's verification independently per instance; nothing
here is a single global cutover.

1. **`auth_sessions`/`auth_users` emptiness check** (read-only, do first,
   every instance): `sqlite3 <datadir>/store/routd.db "SELECT COUNT(*) FROM
auth_sessions; SELECT COUNT(*) FROM auth_users;"`. Verified zero on krons
   at time of writing; sloth and marinade are NOT yet checked — do not run
   steps 3/4 below on an instance until its own count comes back zero.
2. Ship the code fixes (proxyd `sub.Scope`, webd dead-open removal) —
   no schema change, safe on all three regardless of row counts.
3. Ship the `auth_sessions` drop + `auth_users`→`user_profiles` rename as
   routd migrations, gated on step 1's per-instance check passing.
4. Restructure `store/` into per-owner subdirectories (stop each instance's
   daemons, move `.db`+`-wal`+`-shm` triples together, verify `PRAGMA
integrity_check` returns `ok` on each moved file, then bring the instance
   back up on the updated compose + repointed `store.Open`/`OpenRoutd`/
   `OpenOnbod`/authd path constants).

### Ordered, independently-shippable steps

Each ships and reverts on its own; none blocks another except where noted.

1. **webd: drop the dead `messages.db` open** (mirrors `9ff70eef7`).
   Reversible. Verify: `go test ./webd/... -count=1` green + a boot test
   asserting `store.Open` is never called (same pattern as
   `proxyd/audit_sink_test.go`).
2. **proxyd: read `sub.Scope` instead of `groupsForSub`/`UserScopes`.**
   Reversible. **Needs explicit sign-off first** (the 15-minute revocation
   trade-off above). Verify: a test asserting `/priv/<folder>/` access is
   granted from JWT claims alone with `stRoutd` pointed at a DB missing the
   caller's grant row.
3. **Delete `auth_sessions`** (table + `store.CreateAuthSession`/
   `AuthSession` + proxyd's dead cookie branch). NOT reversible (table
   drop) — gated on the per-instance emptiness check. Verify:
   `go test ./proxyd/... ./store/... -count=1` green, `grep -rn
"auth_sessions" --include=*.go .` returns only the migration file and
   the split-cutover copy helper's now-obsolete reference (delete that
   too).
4. **Rename `routd.db`'s `auth_users` → `user_profiles`.** Reversible (a
   second rename migration) but data-loss-risky if run before step 1's
   check — gated the same way. Verify: `grep -rn '"auth_users"\|FROM
auth_users\|INTO auth_users' --include=*.go . | grep -v authd/` returns
   zero hits, `go test ./... -short` green.
5. **Add `resreg.Resource.Owner` (`OwnerRoutd`/`OwnerOnbod`).** Reversible,
   independent of everything else. Verify: `go build ./resreg/...` + a
   registry-validation test rejecting a `RowType`-bearing resource with no
   `Owner`.
6. **Audit the 10 not-yet-traced channel adapters' file/DB access**
   (read-only investigation, zero risk). Produces the missing rows of the
   per-daemon mount table above.
7. **Restructure `store/` into per-owner subdirectories + narrow
   `writeSvc`/`svcDef` to named per-daemon mounts.** Reversible (revert
   compose.go + move files back) but needs a maintenance window (daemons
   stopped during the file move to avoid a WAL write racing it). Depends on
   step 6 for the adapters' entries, not on steps 1-5. Verify: `docker
inspect <container> | jq '.[0].Mounts'` matches the intended list per
   daemon; every daemon's `/health` stays green post-restart.

### Not done in this phase, and why

- **Moving `acl`/`acl_membership`/`auth_users` to `auth.db`.** Rejected, not
  deferred — see "The auth decision." Revisit only if a consumer emerges
  whose need authd can serve more cheaply than routd's in-process path.
- **A general `(principal, action, scope, claims, params)` `/v1/authorize`
  endpoint on routd.** Not built — no live caller needs it; every real
  non-owner consumer today is served by the coarse scope already embedded
  in its own verified JWT. Speculative work against no consumer is exactly
  what this phase should not add.
- **`dashd`'s direct multi-DB SQL reads** (`messages.db`+`routd.db`+
  `onbod.db`+`runed.db`). Untouched here — broad DB access is by design for
  the operator console; whether it should read via resreg faces instead of
  raw SQL is a separate, already-tracked concern (BUGS.md "Resource
  identity" sweep), orthogonal to ownership/mount enforcement.
- **`slakd`'s direct `routd.db` read for pane sessions**, and the
  CLAUDE.md write-discipline line it contradicts. Flagged, not resolved:
  either the code moves to routd's HTTP face or the doc gets corrected to
  describe what's actually FS-mounted-legitimate; not decided in this pass.
- **The 10 not-yet-audited channel adapters' mounts.** Step 6 above is a
  precondition, not done here — narrowing a daemon's mount before tracing
  its actual file access is how you break a live daemon.

## REST-face reconciliation (resolved — 2026-07-06)

The agent MCP face of all seven cold-tier resources (`web_routes`,
`routes`, `network_rules`, `scheduled_tasks`, `acl`, `route_tokens`,
`groups`) rides one shared `resreg.Resource` handler with an injected tier
`Gate`. Folding each resource's **REST** face onto that same handler was blocked not by auth —
the scoped self-service check maps cleanly onto an injected REST `Gate`
(`s.verify.Verify` → `hasAnyScope` + `ownsFolder`, reproduced verbatim) —
but by **wire-contract drift** the then-hand-rolled REST handlers carried
that resreg's conventions did not match. Concretely, on `web_routes` the
fold had to reconcile:

1. `GET /v1/web_routes` is entangled with the `?path_prefix=` owner-lookup
   (same method+path; `ServeMux` can't branch on query) — the aux must move
   to its own endpoint before the resreg list can own that path.
2. `DELETE` changes response (`{deleted:bool}` → `{ok:true}`/404) **and**
   would adopt the shared handler's tier-0 widening — a _new_ cross-folder
   delete for tier-0 REST callers (any top-level folder is tier-0).
3. error body differs (`{error}` vs `{error,message}`); 4. list JSON
   differs (`redirect_to` omitempty); 5. the shared handler acts on
   `Caller.Folder`, but the REST face acts on the client-supplied target
   bounded to the subtree — reconciling needs a body-read in `CallerFromHTTP`.

So the REST-face fold was a **contract decision on a security surface**, not
a mechanical cleanup. **Resolved — full-unify shipped.** The user took the
full-unify call, so `web_routes`/`routes`/`scheduled_tasks`/`acl` adopted
resreg's REST conventions (error/list/delete shapes + tier-0 delete widening +
`?path_prefix=` relocated to `/v1/web_routes/owner`) as the new REST contract;
the tier-structural handlers (`routes`, `scheduled_tasks`) got the per-face
`containFn` decouple (agent → tier `AuthorizeStructural`, REST → `ownsFolder`),
which also closed a live cross-tenant task leak (`0d25b687`). `secrets` folded
its REST face afterward as a forwarder (no resreg tx — the enc value must not
ride `audit_log`). `network_rules` stays agent-only — no REST twin to fold.
`route_tokens`' REST face (`server.go handleToken*`) is not yet folded, and
`groups` has no REST twin — the two remaining hand-rolled/absent surfaces.

## Acceptance

- A resource is served by exactly one in-process `resreg` handler; its
  agent MCP tool, its `/v1/<res>` REST endpoint, and its OpenAPI entry all
  route through it — REST under the default `Gate`, agent under the
  injected `mcp:`+tier `Gate`.
- `dashd` admin page for that resource has no CRUD SQL — it calls the face.
- `ipc/ipc.go` has no bespoke handler BODY for that resource — its agent
  tool delegates to the shared handler; only the socket registration + the
  tier authz/visibility scaffolding remain.
- Agent and REST write the same-shape `audit_log` row for the same action.
- Its table lives in one DB; no second daemon `store.Open`s it.
- `make test` green per step; `arizuko apply` round-trips the resource.

## Out of scope

- The `5/17` mechanism itself (shipped).
- Data-model sharpening (`9/2`) and git-as-truth (`9/3`) — the other two
  phase-8 actions, still phase 8; do not smuggle them in here.
