---
status: partial
---

> **2026-05-28.** Resolved design-direction record. Encodes the
> long-session architectural decisions: naming convention (`d`-suffix
> daemons), DAG library layering, top-level `types/` shared-IDs
> package, per-service `<daemon>/api/v1/` contract pattern, DB-ownership
> rule, NO BACKWARD COMPATIBILITY (one-shot cuts). The old Phase B.1
> (per-daemon tables in shared `messages.db`) is **dropped**;
> cross-daemon access goes through each daemon's `api/v1/` HTTP+MCP
> surface. The `SubjectID` alias and a new `arizuko/` package are
> **rejected** in favour of `types/`. **Partial against code**: the
> `types/` package has LANDED (`types/identity.go`: `UserSub`, `Folder`,
> `Tier`, `Scope`); the never-adopted `core/types.go` stopgap aliases were
> removed. Still not implemented: per-daemon migration of cross-boundary
> signatures onto `types.*`, `authd`, and the gated split. Treat this as
> the design-direction record + in-progress audit, not a proposal to debate.

# Daemon genericization

Make each daemon truly standalone and reusable. Today the daemons are
"microservices" only in the sense of separate processes — they share
`messages.db`, share a `go.mod`, and hardcode arizuko concepts
(`folder`, `tier`, `group`, `chat_jid`). This spec lays out what would
have to change for each daemon to be deployable in isolation and usable
for non-arizuko workloads.

This is the **prerequisite** for the federated control API
([5-uniform-mcp-rest.md](5-uniform-mcp-rest.md)) — a generic daemon with
arizuko concepts wired into its types isn't reusable, and the API
contract is more honest once the concepts are factored out.

The discipline that crackbox already follows
([A-orthogonal-components.md](A-orthogonal-components.md)) is the model
**for shippable sibling components** (crackbox today, future spinouts
like a standalone messaging gateway). Inside arizuko itself the looser
rule applies: libraries form a DAG with downward imports only, and each
daemon's `api/v1/` package is the wire contract other daemons may
freely import. The orthogonality grep targets sibling components, not
inter-library imports.

## Naming convention

Locked. Encoded once here so it stops being argued about:

- **Daemons** end in `d`: `gated`, `proxyd`, `timed`, `authd`, `webd`,
  `dashd`, `onbod`, `vited`, `davd`, `ttsd`, and every channel adapter
  (`teled`, `whapd`, `slakd`, `discd`, `mastd`, `bskyd`, `reditd`,
  `emaid`, `twitd`, `linkd`). A daemon is a process, owns at most one
  DB, exposes REST and/or MCP.
- **Short names: a ≤4-letter root + `d`** (`authd`, `gated`, `timed`,
  `dashd`). New daemons follow this — the gated-split products are
  `routd` (router), `runed` (agent runner), `mcpd` (MCP host), **not**
  `routerd`/`agent-runnerd`/`mcp-hostd`. Keep the root recognisable but
  short; the `d` is the daemon marker, not part of the word.
- **Libraries** do not end in `d`: `auth`, `audit`, `obs`, `resreg`,
  `chanlib`, `grants`, `core`, `types`. Imported, never run.
- **No nesting for shared things.** Top-level paths: `arizuko/audit`,
  `arizuko/auth`, `arizuko/types`. Not `arizuko/core/audit`,
  `arizuko/internal/auth`. Anything reachable from multiple daemons
  lives at the module root.
- **Utility packages** follow Go convention: `httputil/`, `strutil/`,
  not `http_utils/`. The `_utils` suffix is a CLAUDE.md fileshape, not a
  package-name shape.
- **Single Go module**: `github.com/kronael/arizuko`. Sibling shippable
  components (crackbox, future gateway/mcpfw) stay inside this module;
  the orthogonality rule below isolates them, not module separation.

## Component layering — DAG, not flat orthogonality

Inside arizuko, libraries form a directed acyclic graph. Downward
imports only; no cycles. Cross-library imports inside a layer are fine;
upward imports from a library into a daemon are not.

```
Layer 0 — zero arizuko-internal deps (stdlib + third-party only):
  types/         IDs only (UserSub, Folder, Tier, Scope). No behavior, no methods.
  obs/           OTLP wiring; already zero arizuko-internal imports.
  core/          Arizuko-domain rich types (Config, Message, Group, Route,
                 Task; JID parsing). May import types/.

Layer 1 — depend on Layer 0:
  audit/         Writes to caller's *sql.Tx; uses types/ for IDs in payloads.
  auth/          JWT verify + middleware; uses types/ for Claims fields;
                 uses authd/api/v1/ for client + shared shapes.

Layer 2 — depend on Layer 0+1:
  resreg/        Orchestrates auth gate + audit row + tx; depends on
                 auth + audit + types.
  chanlib/       Channel protocol + auth middleware; depends on auth + types.
  grants/        ACL query layer. TODO: maybe merge into core/store.

Layer 3 — daemons (gated, authd, proxyd, timed, webd, dashd, onbod,
channel adapters): import everything they need from layers 0–2 plus
other daemons' api/v1/ packages.
```

The orthogonality grep in
[A-orthogonal-components.md](A-orthogonal-components.md) is the
**sibling-component** test, kept separate so the two rules don't blur —
it does not apply to inter-library imports inside arizuko, which may
cross-import freely as long as the DAG holds.

## `types/` — top-level shared IDs

The cross-daemon `api/v1/` packages need to refer to user subjects,
folders, tiers, and scopes without dragging in `core/`'s rich types
(which already depend on stdlib regex, time-parsing, etc.) and without
creating cycles between every daemon's `api/v1/` and `core/`.

`types/` exists to break those cycles:

```go
// types/identity.go
package types

type UserSub string // OAuth subject (Google sub, GitHub user id)
type Folder  string // arizuko folder identifier ("krons", "atlas/support")
type Tier    int    // access tier (0/1/2/3+); legacy, see § Capability vs tier
type Scope   string // ACL scope expression ("messages:*:own_group")
// Add new IDs here as they need to cross daemon boundaries.
```

Rules for `types/`:

- Pure types. No behavior, no methods, no constants beyond zero-values.
- Zero arizuko-internal imports. Stdlib only.
- Imported by every `<daemon>/api/v1/` package and by daemon
  implementations.
- Anything richer (JID parsing, Folder hierarchy semantics, scope
  evaluation) stays in `core/` or in the daemon that owns the
  semantics. `types/` is the boundary, not the implementation.

**Status — `types/` landed (Phase A foundation).** `types/identity.go`
ships `UserSub`, `Folder`, `Tier`, `Scope` as distinct named types, zero
arizuko-internal imports. The earlier `core/types.go` stopgap aliases
(`TenantID`, `SubjectID`, `Scope`, `Folder = TenantID`) were never
adopted — call sites use plain `string`/`int` — so they were removed when
`types/` landed rather than bridged. `core/` keeps the domain-rich types
for in-process use. **Remaining Phase A work (incremental, per-daemon):**
migrate cross-boundary signatures (`folder string`, `tier int`, …) to
`types.Folder` / `types.UserSub` / `types.Scope` as each daemon's
`api/v1/` package is built — there is no live alias bridge, so each move
is a real type change at that boundary.

## Per-service `<daemon>/api/v1/` contract pattern

Every daemon ships a tiny published-contract sub-package:

```
authd/
├── main.go            ← daemon entrypoint
├── handler.go         ← business logic
├── db.go              ← owns auth.db, runs migrations
└── api/
    └── v1/
        ├── types.go   ← wire shapes (…)
        └── client.go  ← thin HTTP wrapper around the API
```

See [1-auth-standalone.md](1-auth-standalone.md) for authd's actual
`api/v1` types.

Contract rules:

- `<daemon>/api/v1/` is the **published-contract package: wire types +
  a thin client** (the client is a thin HTTP wrapper — zero behavior,
  no business logic — so it carries no state beyond a base URL). **Zero
  arizuko-internal imports beyond `types/`.** Versionable; `v2/` lives
  next to `v1/` when the shape breaks.
- **Other daemons import `<daemon>/api/v1/` freely.** This is the
  canonical wire-contract publishing convention; the orthogonality grep
  allows `<daemon>/api/v1/` paths.
- **Internal implementation (`<daemon>/handler.go`, `<daemon>/db.go`,
  etc.) stays off-limits to other packages.** Go's `internal/`
  convention is the optional compile-time enforcement once the surface
  stabilises.
- **REST and MCP are both implemented in the daemon, both use the same
  `api/v1/` types.** MCP is canonical (agent-first); REST is the
  impedance match for non-MCP callers. Shared types ensure agents and
  humans see the same shape (CLAUDE.md § _MCP + REST hand-rolled and
  uniform_).

This pattern lands gradually: do it for a daemon when its API
stabilises, not big-bang. First instance: `authd/api/v1/`
([1-auth-standalone.md](1-auth-standalone.md)). Cadence: gradual
**across** daemons (adopt `api/v1/` when each daemon's API stabilises);
one-shot **within** each daemon's cutover (no dual-API period — per §
NO BACKWARD COMPATIBILITY).

## Database ownership rule

Single source of truth for what owns what:

> "If they own databases → migrations live with them. They can't own
> tables. If more daemons need access, there is none and a REST / MCP
> API has to be put in place." — user, 2026-05-26

Concrete:

- **Daemons own DBs + migrations + REST/MCP APIs.** A daemon that needs
  persistent state opens its own SQLite file, runs its own migrations
  from its own `migrations/` subdir, and exposes the data via its
  `api/v1/` (REST + MCP).
- **Libraries own no DB + no API + no migrations.** A library that
  needs to write rows takes a `*sql.Tx` from the importing daemon and
  writes through it. `audit/` is the model: it writes `audit_log` rows
  on the caller's transaction; the caller's daemon owns the table.
- **Cross-daemon data access goes through the receiving daemon's
  `<daemon>/api/v1/`.** REST for sync, MCP for agents. **Never direct
  SQL across daemons.** A daemon reads its own DB freely; it may not
  reach into another daemon's DB.
- **Shared `messages.db` stays as-is** (gated owns it, everything else
  connects read/write under gated's schema) **until a daemon is
  extracted to its own DB.** At that point that daemon gets its own
  file + its own migrations subdir, and other daemons that used to join
  across now go through its `api/v1/`.

**Phase B.1 is dropped.** The earlier "per-daemon tables in shared DB"
plan invented a hybrid (daemon owns rows but not the file) that
violates the rule above and creates the worst of both worlds: cross-
daemon SQL still possible, but logical ownership pretends to be
isolated. Skip it; go straight from "shared messages.db" to "per-daemon
DB file with api/v1/ access" the first time a daemon needs out.

## NO BACKWARD COMPATIBILITY

User directive, 2026-05-26. Applies across genericization, authd
rollout, package restructuring:

- **One-shot migrations**, not staged. A cutover ships in one release;
  old paths delete in the same release that brings the new ones up.
- **No dual API periods.** No parallel codepaths during transition; no
  `v1` and `v2` running side by side as a migration tool — `v2` ships
  when `v1` is dead.
- **Migrations broadcast via the migrate skill move agents in
  lockstep** (CLAUDE.md § _Shipping changes_). Tag and broadcast travel
  together.
- **Recovery is `git revert`**, not a compatibility layer.

This kills "phased migration" language elsewhere in this spec. The
phase ordering below is **build order**, not a staged rollout: each
step is committed and verified before the next, but the cutover at the
end is one commit.

## HMAC retirement (foreshadows authd)

`PROXYD_HMAC_SECRET` and `CHANNEL_SECRET` both retire in the authd
cutover — a genericization milestone. Per-mechanism detail (what each
secret does today, what replaces it) lives in
[1-auth-standalone.md](1-auth-standalone.md) § _HMAC retirement plan_.

**Sequencing (decided).** `authd` is extracted **standalone first**, in
its own release — it proves the `<daemon>/api/v1/` + `types/` pattern and
becomes the sole token signer (ES256, publishes JWKs). The rest of the
gated split (`routd` / `runed` / `mcpd`) is a **later
release**, not the same cutover. See [1-auth-standalone.md](1-auth-standalone.md).

## What's coupled today (audit)

Snapshot of `import "github.com/kronael/arizuko/<pkg>"` per daemon
(`grep` over `*.go`, excluding test files where noted). Symbol counts
drive the migration cost.

| Daemon                                       | arizuko-internal imports                                                    | Folder | ChatJID | Sender | UserSub           | Standalone-ready bar                                                                                                                                 |
| -------------------------------------------- | --------------------------------------------------------------------------- | ------ | ------- | ------ | ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `davd`                                       | none                                                                        | 0      | 0       | 0      | 0                 | **already there**                                                                                                                                    |
| `ttsd`                                       | none (arizuko only in comment)                                              | 0      | 0       | 0      | 0                 | **already there**                                                                                                                                    |
| `twitd`                                      | none                                                                        | 0      | 0       | 0      | 0                 | already there (adapter w/o internal deps)                                                                                                            |
| `whapd`                                      | none                                                                        | 0      | 0       | 0      | 0                 | already there                                                                                                                                        |
| `slakd`                                      | `chanlib`                                                                   | 0      | 24      | 6      | 0                 | Phase B semantic: PaneSession coupling to gateway concept; not a naming-only fix.                                                                    |
| `discd`                                      | `chanlib`                                                                   | 0      | n/a     | n/a    | 0                 | same pattern as slakd                                                                                                                                |
| `teled`                                      | `chanlib`, `tests/testutils`                                                | 0      | n/a     | n/a    | 0                 | same pattern                                                                                                                                         |
| `mastd`, `bskyd`, `reditd`, `emaid`, `linkd` | `chanlib`                                                                   | low    | low     | low    | 0                 | same pattern                                                                                                                                         |
| `timed`                                      | `core`, `store`                                                             | 0      | 0       | 0      | 0 (3 `chat_jid`)  | move scheduling DB to `timed.db`; expose `timed/api/v1/` for cross-daemon scheduling.                                                                |
| `dashd`                                      | `chanlib`, `diary`, `tests/testutils`, `theme`                              | 1      | 0       | 1      | 0 (16 `chat_jid`) | UI generification (not reusable as a service)                                                                                                        |
| `proxyd`                                     | `auth`, `chanlib`, `core`, `store`, `tests/testutils`                       | 4      | 0       | 0      | 2 (3 Group)       | strip `folder` from route table, make config-driven (realised in [35-proxyd-standalone.md](35-proxyd-standalone.md) "Per-daemon route declarations") |
| `onbod`                                      | `auth`, `chanlib`, `container`, `core`, `store`, `tests/testutils`, `theme` | 1      | 0       | 0      | 82 (2 `chat_jid`) | user-mgmt is generic; invite/admission generic                                                                                                       |
| `webd`                                       | `auth`, `chanlib`, `core`, `store`, `tests/testutils`                       | 47     | 25      | 15     | 4 (11 `chat_jid`) | UI-coupled; chat model arizuko-shaped                                                                                                                |
| `gated`                                      | `api`, `chanreg`, `core`, `gateway`, `store`                                | heavy  | heavy   | heavy  | heavy             | split (see Phase C)                                                                                                                                  |

The audit confirms three groupings:

- **Already standalone-ready** — `davd`, `ttsd`, `twitd`, `whapd`. Zero
  arizuko-internal imports. Promote them to documented standalone
  services in their READMEs; no code change needed.
- **Chanlib-only** — every other channel adapter. The arizuko-specific
  surface is `chanlib`'s message/event types. Generify `chanlib` (one
  place) and the adapters follow without per-adapter work.
- **Stateful daemons** — `timed`, `dashd`, `proxyd`, `onbod`, `webd`,
  `gated`. Each has its own coupling story above.

`proxyd`'s `folder` coupling is the hardcoded route table plus the
per-daemon `*_ADDR` env wiring in `compose/compose.go` — both surface
arizuko's daemon names inside what is otherwise a generic reverse
proxy. The realisation is config-driven routes: every daemon declares
its own `[[proxyd_route]]` block in `template/services/<name>.toml`,
`compose.go` aggregates them into `PROXYD_ROUTES_JSON`, and proxyd's
table becomes derived data (shipped in
[35-proxyd-standalone.md](35-proxyd-standalone.md)). After that change
proxyd carries zero daemon-name knowledge; the residual `Folder`
references survive only in the slink + dav handlers, which are
arizuko-domain features and stay opt-in route handlers.

## Naming decision (legacy: arizuko-tenant terminology)

The earlier rename of `folder` → `tenant_id` is descoped. `types.Folder`
stays the cross-boundary type name; in-process arizuko code keeps using
`Folder`. The point of `types/` is breaking import cycles, not renaming
existing domain concepts. The wire shape in `<daemon>/api/v1/` can
render the field as `tenant_id` while the Go type is `types.Folder`. The
previously-proposed `core.TenantID` reverse alias plus an
`arizuko/domain.go` package are **withdrawn** in favour of `types/`.

## Phase B — semantic decoupling (per-case)

Naming work (Phase A) is mostly cosmetic. The real genericization is
**semantic**: peeling arizuko-domain concepts out of generic primitives.

| Daemon  | Semantic coupling                                                                            | Decoupling                                                                                                                                   |
| ------- | -------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `slakd` | Stores `PaneSession` for "agent pane in Slack" — a gateway concept leaking into the adapter. | Move pane lifecycle into gateway; adapter exposes a generic `pane/api/v1/` surface (open/close/post-as).                                     |
| `gated` | Hosts schema for every other daemon's tables; folder/tier/group baked into messages.         | Phase C split. Pull schema-authority into `routd`'s own DB; agent-run state into `runed`'s own DB; MCP host into `mcpd` (or co-deploy with). |
| `webd`  | UI-shaped query layer assumes folder/tier; chat widget arizuko-flavoured.                    | Acceptance is "imports `types.Folder`, not `core.Group`"; UI replacement is operator's job.                                                  |
| `onbod` | Invite + admission model uses arizuko-domain "tier".                                         | Replace `tier int` with `scope []string` per § _Capability vs tier_.                                                                         |

Design-per-case; no one-size mechanic. Each lands as its own commit,
the daemon's `api/v1/` shipping in the same release.

## Phase C — extraction (one-shot cuts)

When a daemon's coupling is so deep that an in-place refactor doesn't
buy it standalone status, extract it. **One-shot per § NO BACKWARD
COMPATIBILITY:** the extracted daemon ships, the old code deletes, in
the same release.

`gated` is the headline split. **Sequencing + mechanics (decided):**
`authd` is carved out **first, in its own release** (it is the auth spine
and proves the `<daemon>/api/v1/` + `types/` pattern —
[1-auth-standalone.md](1-auth-standalone.md)). The remaining split —
`routd` + `runed` + `mcpd` — is then a **single big-bang multi-DB
cutover**, not one daemon per release: all three carve their tables into
their own DBs in one coordinated migration, the monolithic `messages.db`
schema-authority in `gated` is deleted, and there is **no backward-compat
shim** and no shared-DB interim. The naming follows the 4-letter-root + `d`
convention (`routd`, `runed`, `mcpd`; `authd` already conforms) — see
§ _Naming convention_.

| New daemon | Owns                                                                                                                                                       | Serves `/v1/`                            | Hosts MCP tools                                                                         | What stays out             |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------- | --------------------------------------------------------------------------------------- | -------------------------- |
| `routd`    | `tenants`, `rules`, `events` tables (own DB)                                                                                                               | `tenants`, `rules`, `events`, `subjects` | routing-control tools (`set_routes`, `match_subject`, `tenant.create`)                  | no agent, no chat, no tier |
| `runed`    | container lifecycle, per-spawn state (own DB)                                                                                                              | `spawns`, `spawn_logs`                   | agent-host tools (`spawn`, `kill`, `stream_output`)                                     | no routing logic           |
| `mcpd`     | per-tenant MCP socket, capability-token brokering (downscoped tokens signed by `authd`, see [1-auth-standalone.md](1-auth-standalone.md)), tool federation | `mcp_tokens`                             | aggregates other daemons' MCP tools (`fetch`, `send_reply`, ...) — federated, not local | no domain state            |

`authd` is the **first** gated-split product (its own release, ahead of
the big-bang cutover above), specified in its own
[1-auth-standalone.md](1-auth-standalone.md); cataloged here only as the
extraction target that goes first.

After the split, arizuko-as-product is a composition:

- **arizuko (full)** = `proxyd + authd + routd + runed + mcpd + onbod + webd + dashd + timed + channel adapters`
- **minimal-router** = `proxyd + authd + routd`
- **chatops-platform** = `proxyd + authd + routd + <custom handlerd>` (subscribes to events, no AI)

Open at the time of split (decide during implementation, not here):
does `mcpd` ever ship as a separate process, or always co-deploy
with `runed`? Lean: always co-deploy; spec the boundary for
clarity, ship as one binary.

`ipc/`'s current tool surface partitions cleanly:

- Routing-control (`set_routes`, `add_route`, `match_subject`, …) →
  `routd`.
- Agent-host (`spawn`, container ops) → `runed`.
- Send/reply/post/upload (the chanlib-adjacent fan-out) → a thin
  federation in `mcpd`; each call forwards to the right adapter
  daemon via that adapter's `/v1/*`.

## ContainerRuntime — pluggable sandbox backends

`runed` (Phase C) owns per-spawn lifecycle. Today
[`container/runner.go`](../../container/runner.go) does everything in
one place: docker invocation, MCP socket, mounts, egress register,
stdio plumbing, deadline timers. Splitting the _container-mechanics_
out behind a small interface gives one seam, many backends, one shared
contract test.

Two reference projects already cracked this:

- openclaw-managed-agents — 4-method `ContainerRuntime` interface at
  [`src/runtime/container.ts:93-135`](../../refs/openclaw-managed-agents/src/runtime/container.ts);
  5-assertion shared contract at
  [`src/runtime/container-contract.ts:65-113`](../../refs/openclaw-managed-agents/src/runtime/container-contract.ts).
- hermes-agent — `BaseEnvironment` ABC at
  [`tools/environments/base.py:288`](../../refs/hermes-agent-fresh/tools/environments/base.py)
  with 7 concrete backends (`local`, `docker`, `ssh`, `singularity`,
  `modal`, `daytona`, `vercel_sandbox`) sharing one ABC. Shape is
  `_run_bash` + `cleanup`; rich behaviour lives in the base.

The seam is genuinely portable: openclaw's contract test ports as Go
table-driven tests; hermes' "tiny abstract, fat base" pattern matches
how `Run`'s 800+ lines decompose (mechanics ~150, MCP/mounts/secrets
~650).

### Interface

```go
// In container/runtime.go (new). Existing Input/Output stay as-is.

// Handle identifies a spawned container and exposes its attached stdio.
// Caller writes the Input payload to Stdin; the result lands on Stdout;
// container logs/errors stream through Stderr.
type Handle struct {
    ID     string        // backend-native container id
    Name   string        // stable host-side name (used by stop, logs)
    Stdin  io.WriteCloser
    Stdout io.ReadCloser
    Stderr io.ReadCloser
    Wait   func() error  // blocks until the container exits
}

type ContainerRuntime interface {
    // Spawn creates and starts a container, attaches stdio, returns the
    // Handle. Blocks until started (NOT until "ready" — arizuko's
    // container signals readiness by reading stdin, no separate probe).
    // Honors Input.Egress, secrets, mounts.
    Spawn(ctx context.Context, cfg *core.Config, in Input) (*Handle, error)

    // Stop terminates gracefully (SIGTERM, then SIGKILL after grace).
    // Idempotent: stop on an already-exited or unknown id is a no-op.
    Stop(ctx context.Context, h *Handle, grace time.Duration) error

    // Logs returns a snapshot of stderr+stdout since spawn, bounded by
    // the backend's log-driver retention. NOT streaming — for live
    // tailing, consume Handle.Stderr during the run.
    Logs(ctx context.Context, h *Handle, tail int) (io.ReadCloser, error)
}
```

`WaitForReady` from openclaw doesn't apply: arizuko's per-turn model
has no `/readyz` probe — the container boots, reads JSON from stdin,
runs the turn, exits. Spawn returning successfully IS ready. Network
attachment + crackbox register happen before Spawn returns.

`Run` in `runner.go` becomes the _consumer_: it calls `Spawn`, writes
the payload to `Handle.Stdin`, drains `Handle.Stdout` into `Output`,
manages deadlines via `Stop`. The ~150 lines of docker-cmd / args /
network-attach become `DockerRuntime.Spawn`; everything else (MCP
server, mount build, secret resolution, egress register, deadlines)
stays in `Run` — per-turn orchestration, not container mechanics.

### Backends arizuko could ship

| Backend                         | Status                                                   | Use                                                                                                                           |
| ------------------------------- | -------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `DockerRuntime`                 | today (extract from `runner.go`)                         | production default                                                                                                            |
| `LocalRuntime`                  | new                                                      | dev/test — runs the agent binary directly, no isolation; bypasses network rules                                               |
| `KVMRuntime`                    | dormant ([specs/11/12](../11/12-crackbox-sandboxing.md)) | stronger isolation via crackbox VM lifecycle                                                                                  |
| `SSHRuntime`                    | future                                                   | run on a remote host (hermes pattern, [`tools/environments/ssh.py`](../../refs/hermes-agent-fresh/tools/environments/ssh.py)) |
| `ModalRuntime`/`DaytonaRuntime` | future                                                   | paid managed sandbox; only when a customer needs it                                                                           |

### Contract test

Single Go table-driven harness in `container/runtime_contract_test.go`:
`RunContainerRuntimeContract(t, factory func() ContainerRuntime)`.
Every backend's own `*_test.go` calls it. Five assertions (mirroring
openclaw's
[`container-contract.ts:65-113`](../../refs/openclaw-managed-agents/src/runtime/container-contract.ts),
adapted to arizuko's spawn-per-turn shape):

1. **`spawn_populates_handle`** — `Spawn` returns a `Handle` with
   non-empty `ID`, `Name`, and all three pipes non-nil.
2. **`stop_is_idempotent`** — `Stop` twice on the same handle returns
   nil both times.
3. **`stop_after_natural_exit_noops`** — once `Handle.Wait` returned,
   `Stop` is still a no-op (per-turn containers exit on their own and
   the deadline-stop races with that exit; the `stopOnce` guard exists
   today exactly for this race).
4. **`spawn_stop_spawn_independent`** — two consecutive `Spawn`→`Stop`
   cycles yield distinct `Handle.ID`s; the second is unaffected by the
   first. arizuko doesn't reuse containers across turns, so
   independence is the relevant invariant.
5. **`payload_round_trips`** — writing a known JSON to `Handle.Stdin`
   and reading `Handle.Stdout` yields the agent's response.

A `FakeRuntime` lives next to the interface for tests of code that
_uses_ a runtime (gateway, runed) without spawning real
containers.

### Migration path

One-shot per § NO BACKWARD COMPATIBILITY:

1. Extract `container/runtime.go` (interface + `Handle`).
2. Move docker-CLI mechanics from `runner.go` into
   `container/runtime_docker.go` implementing `ContainerRuntime`.
3. `Run` becomes the seam consumer; `runner.Run(cfg, ...)` wires the
   default `DockerRuntime`.
4. Existing tests carry forward unchanged — they use `Run`, the
   consumer, not the seam.
5. Contract test added; `DockerRuntime` validated against it.
6. `LocalRuntime` added to unblock CI without docker-in-docker; gated
   on `RUNTIME=local`.

### Out of scope

- Live migration between backends mid-turn (one runtime owns the
  container start-to-kill).
- Per-tenant runtime selection (one `runed` = one runtime;
  mixed backends run multiple instances behind different routes).
- Cross-backend state replication.
- Warm-pool reuse across turns (a separate spec if/when it lands).

## Capability vs tier

The old `tier int` lets the grants evaluator compare ints — sub-µs.
Capability-scope strings need a JWT-verify + scope-match per check.

| Step                                                   | Cost (typical) |
| ------------------------------------------------------ | -------------- |
| ES256 verify of a short JWT against cached JWKs        | ~50–100 µs     |
| Scope-list match (`slices.Contains` over ~5–10 scopes) | sub-µs         |
| Tier int compare (legacy)                              | ~1 ns          |

Per-request cost is already dominated by SQLite hits (10–100 µs) and
disk/network; an ES256 verify is the same order of magnitude — invisible
at our throughput. Crypto is **ES256 (asymmetric) from launch**, not
symmetric: `authd` holds the signing key, every other daemon offline-
verifies against cached JWKs. No "HMAC now, ECDSA later" hedge. Verify
mechanics + JWKs publishing belong to
[1-auth-standalone.md](1-auth-standalone.md) § _Offline JWT verify_.

Decision: **capability scopes replace `tier` everywhere.** The perf
delta is real but a non-issue at our throughput.

## Open detail-level items

Surface here; deep-spec on demand. None blocking.

- **JWKs endpoint design + revocation GC.** Detail in
  [1-auth-standalone.md](1-auth-standalone.md).
- **Idempotency keys.** Mutating `api/v1/*` endpoints accept
  `X-Idempotency-Key`; servers dedupe within a window. Per daemon;
  consider a shared `httputil.Idempotent` helper.
- **Tracing.** `X-Turn-Id` propagation across daemon HTTP calls; extend
  each daemon's request middleware (partly wired via `obs.Setup`).
- **Orthogonality grep regex.** Go's RE2 has no negative lookahead;
  match the package root but exclude `<pkg>/api/v1/` via a two-pass
  grep. Detail + the maintained regex live in
  [A-orthogonal-components.md](A-orthogonal-components.md).

## Acceptance tests — what "standalone-ready" means per daemon

One contract test per daemon, in `tests/standalone/<daemon>_test.go`:

| Daemon           | Standalone-ready test                                                                                                                                                                                        |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `davd`           | Boots with `WEBDAV_ROOT=/tmp/data WEBDAV_PORT=8090`; serves PROPFIND; no `arizuko/*` imports in the binary's go-list output.                                                                                 |
| `ttsd`           | Boots with `TTS_BACKEND_URL=http://kokoro:8880`; forwards `/v1/audio/speech`; no arizuko imports.                                                                                                            |
| `proxyd`         | Boots with a one-route TOML pointing at a stub backend; OAuth login round-trips against one provider; no `core.Folder` import in the binary.                                                                 |
| `timed`          | Boots with `DB_PATH=/tmp/timed.db`; schedules and fires one task on a generic webhook URL; `chat_jid` replaced with `types.Folder` end-to-end.                                                               |
| `authd` (first)  | Boots standalone with `DB_PATH=/tmp/authd.db`; generates an ES256 keypair; mints a JWT for a stub OAuth callback; serves public JWKs at `/v1/keys`; another daemon offline-verifies via the `auth/` library. |
| `routd` (later)  | Boots with `DB_PATH=/tmp/routd.db`; accepts an event POST, matches via a rule, returns the matched tenant.                                                                                                   |
| `auth/`          | Verify-only library (no signing key) imported by a sample standalone binary that verifies an ES256 JWT against `authd`'s JWKs endpoint; no `arizuko/*` peer imports beyond `types/` and `authd/api/v1/`.     |
| Channel adapters | Boot against a stub `chanreg` HTTP endpoint; emit an inbound event with `types.Folder` set; no `core.Folder` import.                                                                                         |
| `onbod`          | Boots with a generic invite flow (no `folder` in the token); creates a `tenant` row.                                                                                                                         |
| `webd`           | (Honest:) **not standalone-ready** — UI baked against arizuko chat shapes. Acceptance: passes the static-import check (no `core.Folder`), accepts arizuko-domain bindings via config.                        |
| `dashd`          | (Same as webd:) UI not reusable; acceptance is the static-import check + arizuko bindings via config.                                                                                                        |

Standalone-ready ≠ reusable for other workloads in every case — `webd`
and `dashd` are arizuko-UI by definition. The bar for them is "no
internal package imports beyond `types/` and other daemons' `api/v1/`"
(the DAG discipline); reuse happens via custom UI replacements.

## What this spec is not

- Not a rewrite. Most files move; very few rewrite. The work is
  systematic (identify type, replace, propagate).
- Not a separate-go.mod-per-daemon proposal. One module stays; package
  boundaries enforce import discipline (DAG inside, orthogonality grep
  for sibling components).
- **Not a staged-migration proposal.** Per § NO BACKWARD COMPATIBILITY,
  cutovers are one-shot. The phases below describe build order, not
  parallel-running periods.

## Implementation phases (ordered, one-shot cuts)

1. **Audit** — done in this spec (see § _What's coupled today_). The
   per-daemon table is the work order.
2. **Phase A — naming + `types/` extraction (per-component).** Land
   `types/` at the module root with the ID types — **DONE**
   (`types/identity.go`); migrate each daemon's cross-boundary signatures
   to `types.Folder` / `types.UserSub` / `types.Scope`. **Skip per-daemon
   when the daemon doesn't import `core/store` at all** (the four "already
   there" daemons). No alias bridge exists, so each signature move is a
   real type change at that boundary. One commit per daemon.
3. **Phase B — semantic decoupling.** Per-case (slakd PaneSession,
   gateway folder dependencies, onbod tier→scope). Design-per-daemon;
   one-shot per release.
4. **Phase C — extraction.** Gated split into routd + runed +
   mcpd; authd extracted per
   [1-auth-standalone.md](1-auth-standalone.md); HMAC retirement ships
   in the same release. One-shot per § NO BACKWARD COMPATIBILITY.
5. **`<daemon>/api/v1/` rollout.** Gradual; do it for a daemon when its
   API stabilises. First instance: `authd/api/v1/`.
6. **Federated API** — once Phases 2–5 are far enough,
   [5-uniform-mcp-rest.md](5-uniform-mcp-rest.md) ships against generic
   types and per-daemon ownership.

## Decisions

- **Naming convention**: `d`-suffix for daemons; no nesting for shared
  things; `httputil`-style for utilities. Locked.
- **DAG layering**: libraries layer 0–2; daemons layer 3; downward
  imports only; cross-library imports inside a layer fine. Locked.
- **`types/`**: top-level shared-IDs package; pure types, no behavior.
  Landed (`types/identity.go`: `UserSub`/`Folder`/`Tier`/`Scope`); the
  never-adopted `core/types.go` stopgap aliases were removed. Locked.
- **`<daemon>/api/v1/` pattern**: every daemon publishes a contract
  sub-package importable by others; orthogonality grep allows it.
  Locked.
- **DB ownership**: daemons own DBs + migrations + APIs; libraries own
  none; cross-daemon access via `api/v1/`. Locked (user quote above).
- **NO BACKWARD COMPATIBILITY**: one-shot cutovers; recover via `git
revert`; no dual API periods. Locked (CLAUDE.md + user directive).
- **HMAC retirement**: `PROXYD_HMAC_SECRET` + `CHANNEL_SECRET` both
  delete in the authd release. Locked; detail in
  [1-auth-standalone.md](1-auth-standalone.md).
- **Capability vs tier**: capability scopes win; tier is retired.
- **Phase B.1 dropped**: shared `messages.db` stays until a daemon goes
  to its own DB; no hybrid "shared file, per-daemon prefix" step.
- **gated split**: yes, after Phase B. Spec'd as four logical daemons
  (`routd`, `runed`, `mcpd`, `authd`); physically two to
  four binaries depending on co-deployment.
- **Channel adapters as edge daemons**: yes — they connect to `routd`
  via `/v1/events`. The current chanreg-over-HTTP pattern is already
  that shape; what changes is the message type (generic, not
  `chanlib.InboundMsg`).

## Code pointers

- `types/identity.go` — the shared cross-boundary IDs (`UserSub`,
  `Folder`, `Tier`, `Scope`). Landed.
- `core/types.go` — domain-rich in-process types; the cross-boundary IDs
  now live in `types/` (the unused stopgap aliases were removed).
- `auth/identity.go`, `auth/acl.go`, `grants/` — rule/identity
  evaluation; refactor target: operate on `types.Scope`, not
  `core.Folder` / `core.Tier`.
- `chanlib/chanlib.go` — the cleanest current "generic message"
  abstraction; `chanlib.InboundMsg` becomes the bridge between channel
  adapters and `routd`.
- `gated/main.go` — entry point for the future split.
- `container/runner.go` — the `runed` core.
- [A-orthogonal-components.md](A-orthogonal-components.md) — the
  sibling-component discipline (zero internal-package imports), updated
  to permit `<daemon>/api/v1/` imports.
- [1-auth-standalone.md](1-auth-standalone.md) — `authd` daemon +
  `auth/` library; first instance of the published-contract pattern;
  HMAC retirement detail.
