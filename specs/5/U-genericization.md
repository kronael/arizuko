---
status: spec
---

# Daemon genericization

Make each daemon truly standalone and reusable. Today the daemons are
"microservices" only in the sense of separate processes — they share
`messages.db`, share a `go.mod`, and hardcode arizuko concepts
(`folder`, `tier`, `group`, `chat_jid`). This spec lays out what would
have to change for each daemon to be deployable in isolation and
usable for non-arizuko workloads.

This is the **prerequisite** for the federated control API
([R-platform-api.md](R-platform-api.md)) — a generic daemon with
arizuko concepts wired into its types isn't reusable, and the API
contract is more honest once the concepts are factored out.

The discipline that crackbox already follows
([`specs/9/b-orthogonal-components.md`](../9/b-orthogonal-components.md))
is the model: a sibling component lives in the same repo + module,
but its import graph has zero references to arizuko-internal
packages. Apply the same discipline daemon-by-daemon here.

## Why now

Two pressures arrive together:

1. The federated API spec wants per-daemon `/v1/*` surfaces. If the
   surface still talks about `folder` as a first-class concept, only
   arizuko consumers can use it.
2. Several daemons are nearly generic already (`davd`, `ttsd`,
   `timed`, channel adapters via `chanlib`, `auth/`). Pulling the
   arizuko-specific pieces out unlocks reuse with little additional
   work.

## What's coupled today (audit)

Snapshot of `import "github.com/<owner>/arizuko/<pkg>"` per daemon
(`grep` over `*.go`, excluding test files where noted). Plus the
arizuko-specific symbol counts that drive the migration cost.

| Daemon                                       | arizuko-internal imports                                                    | Folder                                                     | ChatJID | Sender  | UserSub           | Standalone-ready bar                                                                                                               |
| -------------------------------------------- | --------------------------------------------------------------------------- | ---------------------------------------------------------- | ------- | ------- | ----------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `davd`                                       | none                                                                        | 0                                                          | 0       | 0       | 0                 | **already there**                                                                                                                  |
| `ttsd`                                       | none (arizuko only in comment)                                              | 0                                                          | 0       | 0       | 0                 | **already there**                                                                                                                  |
| `twitd`                                      | none                                                                        | 0                                                          | 0       | 0       | 0                 | already there (adapter w/o internal deps)                                                                                          |
| `whapd`                                      | none                                                                        | 0                                                          | 0       | 0       | 0                 | already there                                                                                                                      |
| `slakd`                                      | `chanlib`                                                                   | 0                                                          | 24      | 6       | 0                 | swap `chanlib.InboundMsg.ChatJID` → generic                                                                                        |
| `discd`                                      | `chanlib`                                                                   | 0                                                          | n/a     | n/a     | 0                 | same pattern as slakd                                                                                                              |
| `teled`                                      | `chanlib`, `tests/testutils`                                                | 0                                                          | n/a     | n/a     | 0                 | same pattern                                                                                                                       |
| `mastd`, `bskyd`, `reditd`, `emaid`, `linkd` | `chanlib`                                                                   | low                                                        | low     | low     | 0                 | same pattern                                                                                                                       |
| `timed`                                      | `core`, `store`                                                             | 0                                                          | 0       | 0       | 0 (3 `chat_jid`)  | replace `chat_jid` with `subject_id`                                                                                               |
| `dashd`                                      | `chanlib`, `diary`, `tests/testutils`, `theme`                              | 1                                                          | 0       | 1       | 0 (16 `chat_jid`) | UI generification (not reusable as a service)                                                                                      |
| `proxyd`                                     | `auth`, `chanlib`, `core`, `store`, `tests/testutils`                       | 3 active + 1 comment (`proxyd/main.go:102, 442, 446, 450`) | 0       | 0       | 2 (3 Group)       | strip `folder` from route table, make config-driven (realised in `specs/6/2-proxyd-standalone.md` "Per-daemon route declarations") |
| `onbod`                                      | `auth`, `chanlib`, `container`, `core`, `store`, `tests/testutils`, `theme` | 1                                                          | 0       | 0       | 82 (2 `chat_jid`) | user-mgmt is generic; invite/admission generic                                                                                     |
| `webd`                                       | `auth`, `chanlib`, `core`, `store`, `tests/testutils`                       | 47                                                         | 25      | 15      | 4 (11 `chat_jid`) | UI-coupled; chat model arizuko-shaped                                                                                              |
| `gated`                                      | `api`, `chanreg`, `core`, `gateway`, `store`                                | (heavy)                                                    | (heavy) | (heavy) | (heavy)           | split (see Phase C)                                                                                                                |

Symbol counts are from `grep -hoE '\b(Folder|ChatJID|Sender|UserSub|chat_jid|Group)\b' *.go` excluding tests; deltas after Phase A drive the per-daemon work order.

The audit confirms three groupings:

- **Already standalone-ready** — `davd`, `ttsd`, `twitd`, `whapd`. Zero arizuko-internal imports. Promote them to documented standalone services in their READMEs; no code change needed.
- **Chanlib-only** — every other channel adapter. The arizuko-specific surface is `chanlib`'s message/event types. Generify `chanlib` (one place) and the adapters follow without per-adapter work.
- **Stateful daemons** — `timed`, `dashd`, `proxyd`, `onbod`, `webd`, `gated`. Each has its own coupling story documented above.

`proxyd`'s "10 Folder references" coupling specifically is the
hardcoded route table in `proxyd/main.go:368-511` plus the per-daemon
`*_ADDR` env wiring in `compose/compose.go:451-486` — both surface
arizuko's daemon names and operator-shaped route prefixes inside what
is otherwise a generic reverse proxy. The realisation is config-driven
routes: every daemon declares its own `[[proxyd_route]]` block in
`template/services/<name>.toml`, `compose.go` aggregates them into
`PROXYD_ROUTES_JSON`, and proxyd's table becomes derived data. See
`specs/6/2-proxyd-standalone.md` "Per-daemon route declarations" for
the schema, loader model, and migration plan. After that change
proxyd's source carries zero daemon-name knowledge; the residual
`Folder` references survive only in the slink + dav handlers, which
are arizuko-domain features and stay opt-in route handlers, not
generic primitives.

## Naming decision

**Locked: `tenant_id`** for the opaque-workspace identifier.

Considered:

- `tenant_id` — explicit; common in multi-tenant SaaS literature.
  Doesn't suggest a directory structure or a chat surface.
- `namespace` — overloaded with Kubernetes / container namespaces.
- `scope` — already used for capability scopes (`auth/`); collision.
- `workspace` — overloaded with chat-platform workspaces (Slack).

`tenant_id` it is. arizuko-domain alias: `type Folder = TenantID`.

## Phase A — generic primitives in shared types

Replace arizuko-domain types in shared libraries with generic
primitives. arizuko-domain types move to a thin arizuko-specific
adapter layer.

| Today                             | Generic                            |
| --------------------------------- | ---------------------------------- |
| `folder string` (path with depth) | `tenant_id string` (opaque)        |
| `tier int` (0=root, derived)      | `scope []string` (capability list) |
| `group` (a folder with grants)    | `tenant` (an isolated workspace)   |
| `chat_jid` (`platform:kind/id`)   | `subject_id string` (opaque)       |
| `messages` (chat-shaped row)      | `events` (generic envelope)        |
| `routes` (jid → folder rules)     | `rules` (subject → tenant rules)   |

### Mechanics

Use Go type aliases for source compatibility while call sites
migrate:

```go
// core/types.go
type TenantID = string
type SubjectID = string
type Scope = []string

// arizuko/domain.go (new)
type Folder = TenantID    // alias — same type, different name
type ChatJID = SubjectID  // alias
type Tier = int           // legacy; phase-out target
```

Migration recipe per package, in order, one PR per step:

1. Land the generic name in `core/types.go`.
2. Add the alias in `arizuko/domain.go`.
3. Migrate one daemon's call sites (`s/Folder/TenantID/`,
   `s/ChatJID/SubjectID/`) — keep both names available; daemon
   compiles either way.
4. Once all daemons compile against generic names, retire the alias.

Reversibility: every step is local. Aliases mean a daemon that hasn't
migrated yet keeps working. No big-bang flag day.

Migration semantics: `tier int` is the one exception — it gets _replaced_,
not aliased, because the int comparison and capability-scope
comparison aren't a same-type swap. See "Capability-vs-tier perf"
below for the cost story.

## Phase B — per-daemon DB ownership

Each daemon owns its own schema and migrations. Two staged options:

### B.1 — Scoped tables in shared file (first)

Daemon-name-prefixed tables in `messages.db`; each daemon runs its
own migrations on the file. Simpler, retains cross-table joins where
they're still useful (e.g. dashd reading other daemons' tables for
read-only views).

Migration recipe:

1. New migrations live under `<daemon>/migrations/*.sql`. Each
   daemon's migrations are run by that daemon's main on startup
   under a `<daemon>_*` table-name prefix.
2. `gated` stops being the single migration runner. `store/migrations/`
   stays for genuinely shared tables (`auth_users`, `user_jids`,
   `secrets`) — these are arizuko-domain not daemon-specific.
3. Daemons that want cross-daemon data go through `/v1/*` HTTP, not
   shared SQL joins.

### B.2 — Separate SQLite file per daemon (later)

Full isolation, no joins across daemons, comm only via `/v1/*`.
Cleaner, more work. Trigger condition: a real isolation requirement
appears — e.g. an operator wants to run `gated` on a different host
than `dashd`, or a per-daemon backup/retention policy is needed.
Until then, B.1 is enough.

## Phase C — gated split

`gated` does at least four things: schema authority, message routing,
agent spawning, MCP socket hosting. Split into:

| New daemon      | Owns                                                             | Serves `/v1/`                            | Hosts MCP tools                                                                         | What stays out             |
| --------------- | ---------------------------------------------------------------- | ---------------------------------------- | --------------------------------------------------------------------------------------- | -------------------------- |
| `routerd`       | `tenants`, `rules`, `events` tables                              | `tenants`, `rules`, `events`, `subjects` | routing-control tools (`set_routes`, `match_subject`, `tenant.create`)                  | no agent, no chat, no tier |
| `agent-runnerd` | container lifecycle, per-spawn state                             | `spawns`, `spawn_logs`                   | agent-host tools (`spawn`, `kill`, `stream_output`)                                     | no routing logic           |
| `mcp-hostd`     | per-tenant MCP socket, capability-token minting, tool federation | `mcp_tokens`                             | aggregates other daemons' MCP tools (`fetch`, `send_reply`, ...) — federated, not local | no domain state            |

After the split, arizuko-as-product is a composition:

- **arizuko (full)** = `proxyd + routerd + agent-runnerd + mcp-hostd + onbod + webd + dashd + timed + auth-lib + channel adapters`
- **minimal-router-as-product** = `proxyd + routerd + auth-lib`
- **chatops-platform** = `proxyd + routerd + auth-lib + <custom handlerd>` (subscribes to events, no AI)

Open at the time of split (decide during implementation, not in this
spec): does `mcp-hostd` ever ship as a separate process, or does it
always co-deploy with `agent-runnerd`? Lean: always co-deploy; spec
the boundary for clarity, ship as one binary.

`ipc/`'s current tool surface partitions cleanly:

- Routing-control (`set_routes`, `add_route`, `match_subject`, etc.)
  → `routerd`.
- Agent-host (`spawn`, container ops) → `agent-runnerd`.
- Send/reply/post/upload (the chanlib-adjacent fan-out) → still a
  thin federation in `mcp-hostd`; each call forwards to the right
  adapter daemon via that adapter's `/v1/*`.

## ContainerRuntime — pluggable sandbox backends

`agent-runnerd` (Phase C) owns per-spawn lifecycle. Today
[`container/runner.go:138-974`](../../container/runner.go) does
everything in one place: docker invocation, MCP socket, mounts,
egress register, stdio plumbing, deadline timers. Splitting the
_container-mechanics_ out behind a small interface gives one seam,
many backends, shared contract test.

Two reference projects already cracked this:

- openclaw-managed-agents — 4-method `ContainerRuntime` interface
  at [`src/runtime/container.ts:93-135`](../../refs/openclaw-managed-agents/src/runtime/container.ts);
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
// container logs/errors stream through Stderr. The handle is opaque to
// the caller beyond these three pipes plus the ID for stop/logs ops.
type Handle struct {
    ID     string        // backend-native container id
    Name   string        // stable host-side name (used by stop, logs)
    Stdin  io.WriteCloser
    Stdout io.ReadCloser
    Stderr io.ReadCloser
    Wait   func() error  // blocks until the container exits
}

type ContainerRuntime interface {
    // Spawn creates and starts a container, attaches stdio, and returns
    // the Handle. Blocks until the container is started (NOT until it
    // is "ready" — arizuko's container signals readiness by reading
    // stdin, no separate probe). Honors Input.Egress, secrets, mounts.
    Spawn(ctx context.Context, cfg *core.Config, in Input) (*Handle, error)

    // Stop terminates the container gracefully (SIGTERM, then SIGKILL
    // after grace). Idempotent: stop on an already-exited or
    // unknown id is a no-op, never an error.
    Stop(ctx context.Context, h *Handle, grace time.Duration) error

    // Logs returns a snapshot of stderr+stdout since spawn. Bounded by
    // the backend's log driver retention. NOT streaming — for live
    // tailing, consume Handle.Stderr directly during the run.
    Logs(ctx context.Context, h *Handle, tail int) (io.ReadCloser, error)
}
```

`WaitForReady` from openclaw doesn't apply: arizuko's per-turn model
has no separate `/readyz` probe — the container boots, reads JSON
from stdin, runs the turn, exits. Spawn returning successfully IS
ready. Network attachment + crackbox register happen before Spawn
returns (they fail-fast at spawn-time today).

`Run` in `runner.go` becomes the _consumer_ of `ContainerRuntime`: it
calls `Spawn`, writes the payload to `Handle.Stdin`, drains
`Handle.Stdout` into `Output`, manages deadlines via `Stop`. The
~150 lines of docker-cmd / args / network-attach become
`DockerRuntime.Spawn`. Everything else (MCP server, mount build,
secret resolution, egress register, soft/hard deadlines) stays in
`Run` — it's per-turn orchestration, not container mechanics.

### Backends arizuko could ship

| Backend                         | Status                                      | Use                                                                                                                           |
| ------------------------------- | ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `DockerRuntime`                 | today (extract from `runner.go`)            | production default                                                                                                            |
| `LocalRuntime`                  | new                                         | dev/test — runs the agent binary directly, no isolation; bypasses network rules                                               |
| `KVMRuntime`                    | dormant (specs/9/12-crackbox-sandboxing.md) | stronger isolation via crackbox VM lifecycle                                                                                  |
| `SSHRuntime`                    | future                                      | run on a remote host (hermes pattern, [`tools/environments/ssh.py`](../../refs/hermes-agent-fresh/tools/environments/ssh.py)) |
| `ModalRuntime`/`DaytonaRuntime` | future                                      | paid managed sandbox; only when a customer needs it                                                                           |

### Contract test

Single Go table-driven harness in `container/runtime_contract_test.go`:
`RunContainerRuntimeContract(t, factory func() ContainerRuntime)`.
Every backend's own `*_test.go` calls it. Five assertions (mirroring
openclaw's [`container-contract.ts:65-113`](../../refs/openclaw-managed-agents/src/runtime/container-contract.ts),
adapted to arizuko's spawn-per-turn shape):

1. **`spawn_populates_handle`** — `Spawn` returns a `Handle` with
   non-empty `ID`, `Name`, and all three pipes non-nil. Maps to
   openclaw assertion #1.
2. **`stop_is_idempotent`** — calling `Stop` twice on the same
   handle returns nil both times. Maps to openclaw #3.
3. **`stop_after_natural_exit_noops`** — once `Handle.Wait` has
   returned, `Stop` is still a no-op (does not return an error for
   "no such container"). New for arizuko; per-turn containers exit
   on their own and the deadline-stop races with that exit
   (`runner.go:284-303` `stopOnce` guard exists today exactly for
   this race).
4. **`spawn_stop_spawn_independent`** — two consecutive
   `Spawn`→`Stop` cycles with the same `Input` yield distinct
   `Handle.ID`s and the second cycle is unaffected by the first.
   Replaces openclaw's #5 (label-honoring); arizuko doesn't reuse
   containers across turns so independence is the relevant invariant.
5. **`payload_round_trips`** — writing a known JSON to `Handle.Stdin`
   and reading `Handle.Stdout` yields the agent's response. Maps to
   openclaw #4 (env-honored) — arizuko delivers the spawn-time
   payload via stdin rather than env, but the assertion is the same:
   the backend faithfully delivers the Input to the agent and the
   agent's Output back to the caller.

A `FakeRuntime` lives next to the interface for tests of code that
_uses_ a runtime (today: gateway, agent-runnerd) without spawning
real containers.

### Migration path

1. Extract `container/runtime.go` (interface + `Handle`).
2. Move the docker-CLI mechanics from `runner.go` (lines ~138-200,
   ~284-303, the `stopContainer` closure) into
   `container/runtime_docker.go` implementing `ContainerRuntime`.
3. `Run` becomes `(rt ContainerRuntime).Run(...)` — same signature
   from the caller's view; `runner.Run(cfg, ...)` wires the default
   `DockerRuntime`.
4. Existing tests (`container/*_test.go`, `tests/standalone/`) carry
   forward unchanged — they use `Run`, which is the seam consumer,
   not the seam itself.
5. Contract test added; `DockerRuntime` validated against it.
6. `LocalRuntime` added to unblock CI without docker-in-docker; gated
   on `RUNTIME=local` env var.

### Out of scope

- Live migration between backends mid-turn. The container is created
  by one runtime, owned by one runtime, killed by one runtime.
- Per-tenant runtime selection. One `agent-runnerd` instance, one
  `ContainerRuntime`. Operators wanting mixed backends run multiple
  `agent-runnerd` instances behind different routes.
- Cross-backend state replication. Each backend manages its own
  containers; nothing is shared.
- Warm-pool reuse across turns. Per-turn spawn stays the default
  (see [openclaw deep-read §8](../../tmp/openclaw-managed-deep.md);
  warm pools are a separate spec if/when they land).

## Capability-vs-tier perf

The old `tier int` lets the grants evaluator compare ints — sub-µs.
Capability-scope strings need a JWT-verify + scope-match per check.
Cost model:

| Step                                                   | Cost (typical) |
| ------------------------------------------------------ | -------------- |
| HMAC-SHA256 verify of a short JWT                      | ~5–10 µs       |
| Scope-list match (`slices.Contains` over ~5–10 scopes) | sub-µs         |
| Tier int compare (legacy)                              | ~1 ns          |

For per-request authorization the JWT verify dominates, but per-request
cost is already dominated by SQLite hits (10–100 µs) and disk/network.
A ~10 µs auth check is invisible.

ECDSA verification (~100 µs) is only needed for cross-daemon trust if
keys aren't shared. Today daemons share `AUTH_SECRET`, so HMAC is the
right pick. ECDSA stays a future option if/when daemons run on
separate hosts.

Decision: capability scopes replace `tier` everywhere. The perf
delta is real but non-issue at our throughput.

## Acceptance tests — what "standalone-ready" means per daemon

For each daemon, "standalone-ready" means it satisfies the following
contract test in CI (one per daemon, in `tests/standalone/<daemon>_test.go`):

| Daemon              | Standalone-ready test                                                                                                                                                                 |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `davd`              | Boots with `WEBDAV_ROOT=/tmp/data WEBDAV_PORT=8090`; serves PROPFIND; no `arizuko/*` imports in the binary's go-list output.                                                          |
| `ttsd`              | Boots with `TTS_BACKEND_URL=http://kokoro:8880`; forwards `/v1/audio/speech`; no arizuko imports.                                                                                     |
| `proxyd`            | Boots with a one-route TOML pointing at a stub backend; OAuth login round-trips against one provider; no `core.Folder` import in the binary.                                          |
| `timed`             | Boots with `DB_PATH=/tmp/timed.db`; schedules and fires one task on a generic webhook URL; `chat_jid` replaced with `subject_id` end-to-end.                                          |
| `routerd` (planned) | Boots with `DB_PATH=/tmp/routerd.db`; accepts an event POST, matches via a rule, returns the matched tenant.                                                                          |
| `auth/`             | Library imported by a sample standalone binary that mints + verifies a JWT; no `arizuko/*` peer imports.                                                                              |
| Channel adapters    | Boot against a stub `chanreg` HTTP endpoint; emit an inbound event with `SubjectID` set; no `core.Folder` import.                                                                     |
| `onbod`             | Boots with a generic invite flow (no `folder` in the token); creates a `tenant` row.                                                                                                  |
| `webd`              | (Honest:) **not standalone-ready** — UI baked against arizuko chat shapes. Acceptance: passes the static-import check (no `core.Folder`), accepts arizuko-domain bindings via config. |
| `dashd`             | (Same as webd:) UI not reusable; acceptance is the static-import check + arizuko bindings via config.                                                                                 |

Standalone-ready ≠ reusable for other workloads in every case — `webd`
and `dashd` are arizuko-UI by definition. The bar for them is "no
internal package imports" (the crackbox discipline); reuse happens via
custom UI replacements, not via reusing dashd.

## What this spec is not

- Not a rewrite. Most files move; very few rewrite. The work is
  systematic (identify type, replace, propagate).
- Not a separate-go.mod-per-daemon proposal. One module stays;
  package boundaries enforce import discipline. (Same rule
  `specs/9/b-orthogonal-components.md` uses for crackbox.)
- Not breaking compatibility. Migrations carry forward; the generic
  shapes are aliases for the existing types until callers swap over.

## Implementation phases (ordered)

1. **Audit** — done in this spec (see "What's coupled today"). The
   per-daemon table is the work order.
2. **Phase A — generic types** — introduce generic shapes in `core/`;
   arizuko-specific aliases in `arizuko/domain.go`. No behaviour
   change. Migrate daemon-by-daemon, lightest first
   (`davd`/`ttsd`/`twitd`/`whapd` are already done; channel adapters
   via `chanlib` next; then `timed`; then heavier daemons).
3. **Phase B.1 — scoped tables** — each daemon owns its tables and
   migrations within `messages.db`. gated stops migrating others'
   tables.
4. **Phase C — gated split** — extract `routerd` and `agent-runnerd`.
   Keep deployable as a single binary (`arizuko-monolith`) for ops
   simplicity during cutover.
5. **Federated API** — at this point
   [R-platform-api.md](R-platform-api.md) ships against generic types
   and per-daemon ownership. The contract is honest.
6. **Phase B.2 — separate DBs** — if/when isolation across hosts
   becomes a real need.

## Decisions

- **Naming**: `tenant_id` (see "Naming decision" above).
- **Capability vs tier**: capability scopes win; tier is retired.
- **DB split**: B.1 (scoped tables in shared file) first; B.2
  (separate files) only on demonstrated need.
- **gated split**: yes, after Phase B. Spec'd as three logical
  daemons; physically two binaries (mcp-hostd folds into
  agent-runnerd unless co-deployment becomes inconvenient).
- **Channel adapters as edge daemons**: yes — they connect to
  `routerd` via `/v1/events`. The current chanreg-over-HTTP pattern
  is already that shape; what changes is the message type (generic,
  not `chanlib.InboundMsg`).

## Code pointers

- `core/types.go` — current shared types; grows generic shapes.
- `auth/identity.go`, `auth/acl.go`, `grants/` — current
  rule/identity evaluation. Refactor target: operate on
  `Scope []string`, not `Folder`/`Tier`.
- `chanlib/chanlib.go` — the cleanest current "generic message"
  abstraction; closest to the target shape. `chanlib.InboundMsg`
  becomes the bridge between channel adapters and `routerd`.
- `gated/main.go` — entry point for the future split.
- `container/runner.go` — the `agent-runnerd` core; already factored
  enough to move.
- `specs/9/b-orthogonal-components.md` — the discipline this spec
  adopts (zero internal-package imports per shippable component).
