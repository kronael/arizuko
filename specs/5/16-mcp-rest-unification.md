---
status: partial
depends: [5/13-ext-mcp, 5/17-openapi-mcp, 5/32-acl-unified]
moved_from: specs/9/index.md §1 (was "phase 8 action 1"; pulled to phase 5)
---

> **Status (2026-08-06).** Still `partial`. Adoption is done; of what remains,
> **federation shipped and "one owner" did not.**
>
> All seven agent-facing cold-tier resources — `web_routes`, `routes`,
> `network_rules`, `scheduled_tasks`, `acl`, `route_tokens`, `groups` — ride one
> `resreg.Resource`, and each wears its REST twin on the same handler (plus
> onbod's `/v1/invites` + `/v1/gates`). Two deliberate exceptions: `secrets` is
> REST-only and write-only (the encrypted value must never ride the agent
> surface); `network_rules` is agent-only. `groups`' REST twin is LIST-only —
> operator CREATE stays dashd's FS-managed `SetupGroup`, so routd opens no second
> bare create door. `mcp_connectors` was floated and CUT: connectors already load
> from `<datadir>/connectors.toml`, and a resreg resource would add a second
> source of truth.
>
> **Federation, shipped 2026-08-06 by [`5/I`](I-tool-call-logging.md).** The
> `audit` resource is ONE read-only registration (`resreg/resources/audit.go`)
> mounted by THREE daemons over three separate SQLite files —
> `routd/audit_resource.go`, `runed/audit_resource.go`, `authd/audit_resource.go`
> — each serving `GET /v1/audit` through one reader, `audit.Query`
> (`audit/query.go`). `/dash/audit/` (`dashd/audit_page.go`) fans out to all three
> over HTTP with a composite cursor and a per-source failure banner, and it stopped
> reading `routd.db` directly to do it. That is this spec's federation clause —
> cross-daemon reads call the owner's face over HTTP, never a second `store.Open` —
> working across daemon and DB-file boundaries.
>
> **It is not evidence for the owner clause, and does not close this spec.**
> `audit_log` is the one resource deliberately replicated per owner DB; its
> registration sets `DB: ""` precisely because "the table lives in four owner DBs
> at once, so no single subsystem key is true" (`resreg/resources/audit.go:93`).
> A resource that opts OUT of one-owner cannot demonstrate one-owner. The fan-out
> is 3-of-4 besides: onbod owns an `audit_log` and writes real rows to it, and its
> read face is unbuilt (BUGS `F35`).
>
> **The advertised set is now derived from the mux** (`F33`, closed 2026-08-07).
> `resreg.OpenAPIHandler(daemon, mux)` takes the routing table and nothing else:
> `RegisterREST` mounts each face as a `*restMount` stamped with its
> `(resource, endpoint)`, and `resreg.MountedResources` keeps only the registry
> resources the mux resolves to one of those. All six hand-passed name lists are
> deleted. Advertised-vs-served is no longer two declarations agreeing by care —
> there is one, so `F21`/`F27`/`F32` are unrepresentable rather than merely
> tested for.
>
> Derivation is by mount IDENTITY, never by probing paths. Path-probing was
> considered and is WRONG here: THREE daemons serve `GET /v1/sessions` over three
> different tables — authd's refresh-token families (the resreg resource), runed's
> `session_log` (`runed/server.go:44`), routd's `core.SessionRecord`
> (`routd/server.go:286`) — so a path probe would make routd and runed publish
> authd's `SessionsRow` schema. (`F46` files this collision as two daemons; it is
> three.) The type assertion drops both hand-rolled mounts, and proxyd's `/`
> catch-all with them. The one resulting doc change was `+ DELETE
/v1/acl_membership` on routd, which routd genuinely mounts
> (`routd/membership_resource.go`) and never advertised.
>
> **What "one owner" still needs** — the remaining gap is the mount `Name`, not
> the doc:
>
> - The mounted handler must DERIVE `Name`/`Table` from the registry, not restate
>   them. `Endpoints`/`RowType`/MCP metadata already single-source by import;
>   `Name` is still a string literal at all ~11 mount sites. `MountedResources`
>   now compares mounted `Name` to registry `Name` on every face, so a mistyped
>   literal silently drops the resource from the doc instead of mislabelling it —
>   a fail-safe, not the fix.
> - Step 2 below is unshipped. Its violation is a cross-daemon direct-DB READ of
>   another owner's table, NOT a duplicate handle: `proxyd` opens `routd.db`
>   exactly once (`store.OpenRoutd`, `proxyd/main.go:991`) and reuses that handle,
>   so "never a second `store.Open`" is not what it breaks. What it breaks is
>   federation — `proxyd/main.go:863` reads routd's `acl` via
>   `auth.UserScopes(s.stRoutd, …)` for a scope list its verified token already
>   carries, instead of calling the owner's face.
> - Step 7 is half-shipped: `dataMounts`/`dataSubdirs` exist
>   (`compose/compose.go:840`) but only onbod, proxyd and webd set them, and they
>   name `store`/`groups`/`web` — not per-owner `store/<owner>/`. "Owner is a
>   convention, not a boundary" still holds for every other daemon.
>
> **The per-daemon guard changed shape with the cause fix** (`F40`, closed;
> `F47`, closed). `AssertServesWhatItAdvertises` compared the document to the
> mux — a comparison deriving the document FROM the mux makes tautological, and a
> guard that cannot fail is worse than no guard. It is replaced by
> `resregtest.AssertAdvertises(daemon, mux, want)`, which pins each daemon's whole
> derived surface against a list written down in its test: mounting or unmounting
> any REST face fails it until someone states the new surface, so the change stays
> visible in review. The hand-written list is a test EXPECTATION, never a
> production input — that distinction is the whole of `F33`.
> `AssertServesNoneOf` did NOT weaken and is unchanged: nothing about deriving
> from the mux stops two daemons mounting one resource name.
>
> The four drift shapes are proven falsifiable in `resreg/openapi_mount_test.go`,
> which reintroduces each one — unmounted advertise, re-path, catch-all, foreign
> hand-rolled mount — and pins that `MountedResources` drops it while the
> by-name rendering still shows it. That second half is the anchor: without it a
> test passes when the emitter produces nothing.
>
> Earlier wiring notes that still hold: `authd` registered `/openapi.json`,
> `/auth/*` and `/metrics` in `main()` after `srv.mux()` returned, so a guard
> would have probed a mux missing the mount under test; `mux()` is now the
> complete served surface. And OpenAPI 3.1 has no multi-segment path template, so
> a stdlib `{path...}` documents as `{path}`; the rule is single-sourced as
> `resreg.OpenAPIPathKey`, with `resreg.ConcretePath` its inverse for probing.
>
> **This spec stays `partial`** until steps 2, 6 and 7 land. Step 2 needs the
> explicit sign-off it has always needed (the 15-minute revocation trade-off),
> step 6 is an unstarted read-only audit, step 7 needs a maintenance window for
> the file move. None is blocked by the doc work above.

# specs/5/16 — MCP+REST unification (finish the adoption)

[`5/17-openapi-mcp`](17-openapi-mcp.md) is the **mechanism** — one
`resreg.Resource` per `(resource, action)` wearing two faces: the agent's
MCP tools (derived by `deriveMCPTools`) and the human's annotated REST
`/openapi.json`. This spec is the **adoption program**: migrate every
resource onto that mechanism and delete the parallel hand-rolled surfaces.
Orthogonal to `5/17` — mechanism vs rollout.

## The target

One cold-tier handler, **two faces from one `resreg.Resource`** ([`5/17`](17-openapi-mcp.md)
owns the mechanism), one owner:

- **Hot-tier agent actions** (`reply`/`send`/`inspect_*`) stay MCP-only and
  hand-authored in `ipc/ipc.go` — there is no REST resource to derive them from.
  `ipc.go` doesn't disappear; it loses its management tool bodies and keeps the
  hot-tier tools plus the unix-socket transport.
- **Both faces is the default, not a universal.** Security-driven exceptions
  stand and are intended (`secrets` REST-only, `network_rules` agent-only).
- **One owner + federation**: each resource's table lives in exactly one
  daemon's DB; cross-daemon reads call the owner's face over HTTP — the owner
  resolves by compose service naming (`<DAEMON>_URL`), never a second
  `store.Open`. This retires `messages.db` as a shared 8th DB.

The orthogonal sibling concern is [`5/8`](8-yaml-manifests.md) — the same tables
as declarative YAML you `export`/`apply`. 5/16 is the runtime surface, 5/8 the
manifest transport; the row-level plumbing under both is not a concept worth
naming.

## Why the agent face needed a seam of its own

`proxyd_routes` proved the two-face mechanism only on the OPERATOR socket: a
global forwarder, callers holding a `**` ACL row, no folder containment. The
agent socket is per-folder and must bind every call to a target, so a resource
mounted there cannot simply reuse resreg's operator gate — `defaultGate`
(`resreg/resreg.go:572`) checks a `<resource>:<action>` scope the agent
principal never holds, and would deny every folder agent.

**Decision: resreg stays policy-free and the authorization is injected per
surface.** Two paths were rejected — (A) re-implement the agent's decision
inside `resreg.invoke`, which puts policy in the engine; (B) share only the
handler and keep the agent MCP tools hand-rolled, which keeps the duplication
this spec exists to remove. The shipped third path is a `Gate` seam plus a
containment seam:

- The agent socket sets `agentAllowGate`, a **no-op** resreg `Gate`
  (`routd/agent_gate.go:65`), so the operator `defaultGate` does not run there.
- The real check rides each handler's injected `containFn`
  (`routd/routes_resource.go:85`): the agent face resolves the target from the
  args and calls `auth.Authorize` on it; the REST face calls `ownsFolder` on the
  JWT folder claim.
- Visibility is a separate view: `auth.EffectiveActions` over the caller's ACL
  rows (`routd/agent_gate.go:52`), not the gate.
- The facade is in-process, not a second server — the agent tool is a generated
  thin facade over the SAME handler, so the one-audit-row-in-the-mutation's-tx
  invariant holds.

**This section's earlier tier framing is dead.** It described the agent gate as
a tier-keyed fallback (`AuthorizeOpts{Folder,WorldFolder,Tier}`, a `grants`
package, a tier-capping `auth.AuthorizeStructural`). Tiers dissolved in
[`5/33`](33-paths-roles.md): there is one evaluator, `auth.Authorize`
(`auth/authorize.go:25`), over ACL rows, deny-wins, with no depth-derived
fallback. `AuthorizeOpts`, `auth.AuthorizeStructural`, and the `grants` package
have zero definitions and zero call sites in the tree. Stale comments naming
them survive in `ipc/ipc.go` and several `_test.go` files — do not read those as
evidence. The surviving lowercase `authzStructural` (`ipc/ipc.go:931`) is a
different thing: a local closure for the hand-authored hot-tier tools that
calls the same one evaluator on the target, carrying no tier.

## Two resources share the label `routes` (don't conflate them)

Message-routing `routes` is routd's match→target-folder table
(`routd/routes_resource.go`); `proxyd_routes` is proxyd's HTTP reverse-proxy
table. Both wear both faces; neither collapses into the other. A resource's name
IS its wire identity, so this pair must never converge on one name — proxyd's
live resource once drifted to `Name: "routes"` while its own catalog and
OpenAPI said `proxyd_routes` (fixed 2026-07-01).

## Invariants the migration established

The per-resource rollout is done; two rules it produced outlive it:

- **Reconcile the audit shape.** Agent and REST must write the SAME `audit_log`
  row for the same action, so a `grep 'action=<verb>'` finds the work regardless
  of surface. Drift here re-splits the surfaces the fold just merged.
- **Delete only the per-tool BODY.** The agent socket's registration, its
  visibility view, and every hot-tier tool stay; only the management tool bodies
  collapse into the shared handler.

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
is a **shared-identity test**, and it now ships:
`routd/endpoints_source_test.go`'s `TestResourceEndpoints_SingleSource` looks
each mounted resource up by its own `Name` and compares against what the
registry publishes under it, so an unregistered name and a drifted endpoint set
both fail. Nothing here claims compile-time enforcement; that claim is dropped.
The per-action coverage assertion remains unbuilt — `Handler` still switches on
a hand-written case list.

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
**owner DB** (who migrates the table) are different axes. `resreg.Resource`
carries this today as `DB` (`resreg/resreg.go:274`), keyed by
`SubsystemRoutd`/`SubsystemOnbod` (`resreg/resreg.go:66-72`) — the logical
owner, not a filesystem path and not the serving daemon; `BySubsystem`
(`resreg/engine.go:1141`) is what `export`/`plan`/`apply` walk. An empty `DB`
means "no single owner", which is why `audit`, `sessions` and `signing_keys`
set it and are therefore never round-tripped through a manifest.

`authd`'s `auth.db` is a **separate owner**, outside this table: it owns
`oauth_identities`, `refresh_tokens`, `signing_keys`, `auth_users` — none of
these are resreg-managed cold-tier resources (no agent/REST CRUD face), so
they don't belong in the resource owner-DB map; they're identity
infrastructure. (`identities`/`identity_claims` — an advisory cross-channel
claim axis with no live writer — were dropped 2026-08-04, `fcd845cb`; binding
a channel identity to a person is `5/31` pairing now.) `authd`'s `auth_users`
(`user_id TEXT PRIMARY KEY`, anchors `oauth_identities` FK,
`authd/migrations/0001-authd-schema.sql:22-26`) and `routd.db`'s former
`auth_users` (`sub TEXT UNIQUE`, carried cost caps,
`routd/migrations/0011-cost-log-user-sub.sql:16-25`) are **different tables
that happened to share a name** — different PK, different purpose, both
live, both empty on krons at time of writing (verify on every instance
before the rename below). The fix is renaming routd's copy, not merging the
schemas; see the auth decision.

### The auth decision — routd stays the policy decision point

`auth.Authorize(s *store.Store, caller, action, scope, params)`
(`auth/authorize.go:25`) is the sole runtime ACL evaluator and it needs a
LOCAL `*store.Store` — `resreg`'s `defaultGate` (`resreg/resreg.go:572`)
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
2. `auth_sessions` (`routd.db`) — **DONE** (`db1e6f3c`, 2026-08-02). The table,
   `store.CreateAuthSession`, and proxyd's cookie branch that read it are all
   deleted. The branch was dead: authd's OAuth flow writes a `refresh_tokens`
   row in `auth.db`, never a `routd.db` `auth_sessions` row, so the lookup
   always missed and `requireAuth` fell through to `tryRefreshViaAuthd` — the
   live path. Two premises stated here originally were wrong and are corrected
   for the record: the table DID have a production writer
   (`routd/db.go:110` copied it from legacy `messages.db` on every routd boot,
   removed in the same commit), and it was NOT empty everywhere — sloth held 5
   rows and marinade 3, all long expired. The backfill had to stop before the
   drop, not after.
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

1. **webd: drop the dead `messages.db` open** (mirrors `9ff70eef7`) — **DONE**.
   `webd/main.go:64` opens `store.OpenRoutd` and nothing else;
   `webd/audit_sink_test.go` pins the one-store shape, citing this section.
2. **proxyd: read `sub.Scope` instead of `groupsForSub`/`UserScopes`** — **OPEN,
   and the last live violation of this section's federation clause**.
   `proxyd/main.go:863` still calls `auth.UserScopes(s.stRoutd, …)`, reading
   another owner's `acl` table for a list the verified token already carries.
   It is a cross-daemon direct-DB READ, not a duplicate handle: proxyd opens
   `routd.db` once at `proxyd/main.go:991` and reuses it (`LookupRouteToken` at
   line 729 is the same pattern and the same violation). Reversible.
   **Needs explicit sign-off first** (the 15-minute revocation trade-off above).
   Verify: a test asserting `/priv/<folder>/` access is granted from JWT claims
   alone with `stRoutd` pointed at a DB missing the caller's grant row.
3. **Delete `auth_sessions`** (table + `store.CreateAuthSession`/
   `AuthSession` + proxyd's dead cookie branch) — **DONE**,
   `routd/migrations/0024-drop-auth-sessions.sql`, pinned by
   `routd/migrations_0015_test.go`. The surviving hits are the frozen
   `store/migrations/0001-initial-schema.sql` and the split-cutover copy list
   (`cmd/arizuko/migrate_split.go:297`), both reading legacy `messages.db`.
4. **Rename `routd.db`'s `auth_users` → `user_profiles`** — **DONE**. The only
   `auth_users` reference left outside `authd/` is the split-cutover source
   mapping (`cmd/arizuko/migrate_split.go:150`), which is the rename itself.
5. **Add a typed owner to `resreg.Resource`** — **DONE** as `DB`
   (`resreg/resreg.go:274`), keyed by `SubsystemRoutd`/`SubsystemOnbod`; empty
   means "no single owner" and drops the resource from `BySubsystem`, i.e. out of
   `export`/`plan`/`apply`.
6. **Audit the 10 not-yet-traced channel adapters' file/DB access**
   (read-only investigation, zero risk). Produces the missing rows of the
   per-daemon mount table above.
7. **Restructure `store/` into per-owner subdirectories + narrow
   `writeSvc`/`svcDef` to named per-daemon mounts** — **HALF DONE**. The
   narrowing mechanism ships: `svcDef.dataSubdirs` + `dataMounts`
   (`compose/compose.go:840-855`) replace the blanket bind for the daemons that
   set it — today only `onbod` (`store`,`groups`,`web`), `proxyd` (`store`) and
   `webd` (`store`). The `store/<owner>/` restructure itself is NOT done: the
   subdir names are `store`/`groups`/`web`, so a narrowed daemon still sees every
   owner's DB file, and `ONBOD_DB_PATH` is still flat `store/onbod.db`. Until the
   restructure lands, "owner is a convention, not a boundary" stands. Reversible
   (revert compose.go + move files back) but needs a maintenance window (daemons
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

Folding each resource's REST face onto the shared handler was blocked not by
auth but by **wire-contract drift**: the hand-rolled REST handlers carried
response shapes, error bodies, and query-entangled paths that resreg's
conventions did not match. That made the fold a contract decision on a security
surface, not a mechanical cleanup — adopting the shared handler's wider delete
scope would have handed REST callers a cross-folder delete they never had.

**Resolved: full-unify.** The REST faces adopted resreg's conventions as the new
contract (`?path_prefix=` relocated to `/v1/web_routes/owner`), and the
structural handlers got the per-face `containFn` decouple. `secrets` folded as a
forwarder with no resreg tx, because the encrypted value must not ride
`audit_log`.

**The failure this produced, and the fix.** The delete/list widening was first
keyed on tier-0 — but a NAMED top-level tenant is also tier-0, so the widening
let one tenant delete and list a sibling tenant's rows. Live cross-tenant leak,
closed by re-keying the widen on the **empty folder claim** (the genuine
root/operator caller) rather than on depth: `routd/web_routes_resource.go:135`
records it, `routd/web_routes_rest_test.go:108-165` pins it. This is the concrete
reason depth-derived tiers are gone — a rank recomputed from a path cannot
distinguish "operator" from "top-level tenant".

## Acceptance

- A resource is served by exactly one in-process `resreg` handler; its
  agent MCP tool, its `/v1/<res>` REST endpoint, and its OpenAPI entry all
  route through it — REST under the default `Gate`, the agent under the
  no-op gate plus its `containFn`.
- `dashd` admin page for that resource has no CRUD SQL — it calls the face.
- `ipc/ipc.go` has no bespoke handler BODY for that resource.
- Agent and REST write the same-shape `audit_log` row for the same action.
- Its table lives in one DB; no second daemon `store.Open`s it.
- `make test` green per step; `arizuko apply` round-trips the resource.

## Out of scope

- The `5/17` mechanism itself (shipped).
- Data-model sharpening (`9/2`) and git-as-truth (`9/3`) — the other two
  phase-8 actions, still phase 8; do not smuggle them in here.
