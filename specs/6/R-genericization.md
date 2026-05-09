---
status: draft
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

## Why now

Two pressures arrive together:

1. The federated API spec wants per-daemon `/v1/*` surfaces. If the
   surface still talks about `folder` as a first-class concept, only
   arizuko consumers can use it.
2. Several daemons are nearly generic already (`timed`, `proxyd`,
   `auth/`). Pulling the arizuko-specific pieces out unlocks reuse
   with little additional work.

## What's coupled today

| Coupling                                                   | Severity | Where                                                  |
| ---------------------------------------------------------- | -------- | ------------------------------------------------------ |
| Shared SQLite DB (`messages.db`)                           | high     | every daemon opens the same file                       |
| Shared `go.mod` + arizuko-internal package imports         | high     | every daemon imports `core/`, `store/`, `chanlib/`     |
| Hardcoded `folder` concept                                 | high     | timed.target, ipc.scope, webd routes, dashd queries    |
| Hardcoded `tier` concept                                   | medium   | grants evaluator, MCP tool gating                      |
| `chat_jid`, `group`, `agent` semantics in daemon types     | medium   | gated routing, ipc tools, channel adapters             |
| Schema migrations owned by gated alone                     | medium   | other daemons can't run without gated having run first |
| `gated` is a multi-purpose monolith (router + spawn + MCP) | medium   | one binary owns 3+ distinct responsibilities           |

## Target shape

Two phases of decoupling, in this order:

### Phase A — generic primitives in shared types

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

`core/types.go` keeps generic shapes; `arizuko/domain.go` (new)
provides the arizuko-specific bindings (`folder = tenant_id`, etc.).
Daemons import generic shapes only; arizuko-specific behaviour lives
in handlers and migrations.

### Phase B — per-daemon DB ownership

Each daemon owns its own schema and migrations. Either:

- **Same SQLite file, scoped tables** — daemon-name-prefixed tables;
  each daemon runs its own migrations on the file. Simpler, retains
  cross-table joins where they're still useful.
- **Separate SQLite file per daemon** — full isolation, no joins
  across daemons, comm only via `/v1/*`. Cleaner, more work.

Lean: scoped tables in shared file as Phase B.1; full per-daemon DBs
as Phase B.2 if/when isolation requirements demand it (e.g. running
gated on a different host).

### Phase C — gated split

`gated` does at least three things: schema authority, message routing,
agent spawning, MCP socket hosting. Split into:

- **`routerd`** — generic multi-tenant message router. Owns
  `tenants`, `rules`, `events`. No `agent`, no `chat`, no `tier`. The
  reusable core: a webhook router, chatops platform, message bus
  with capability auth.
- **`agent-runnerd`** — arizuko-specific. Subscribes to routerd
  events, spawns containers per turn, writes results back. The AI
  layer.
- **`mcp-hostd`** — per-tenant MCP socket host. Mints agent
  capability tokens, federates tool calls to other daemons. Could
  fold into `agent-runnerd` if always co-deployed; spec'd separately
  to keep ownership crisp.

After the split:

- arizuko-as-product = `proxyd + routerd + agent-runnerd + mcp-hostd
  - onbod + webd + dashd + timed + auth-lib + channel adapters`
- minimal-router-as-product = `proxyd + routerd + auth-lib`
- chatops-platform = `proxyd + routerd + auth-lib + custom-handlerd`

## What's reusable, in standalone-readiness order

| Daemon                    | Reusable today | Path to standalone                                                           |
| ------------------------- | -------------- | ---------------------------------------------------------------------------- |
| `auth/`                   | yes (library)  | already generic; promote to standalone module possible                       |
| `proxyd`                  | mostly         | strip arizuko-specific route table; make config-driven                       |
| `timed`                   | mostly         | replace `folder` with `tenant_id`; target arbitrary URLs                     |
| `routerd`                 | future         | extract from `gated`; depends on Phase C                                     |
| `onbod`                   | partial        | invite/admission flows are generic; user-mgmt portion likely arizuko-coupled |
| `webd`                    | low            | chat-UI specific to arizuko's message model                                  |
| `dashd`                   | low            | UI for arizuko concepts; not naturally reusable                              |
| Channel adapters          | low            | glue; meaningful only paired with the routing core                           |
| `agent-runnerd` (planned) | n/a            | arizuko-specific by definition                                               |

## What this spec is not

- Not a rewrite. Most files move; very few rewrite. The work is
  systematic (identify type, replace, propagate).
- Not a separate-go.mod-per-daemon proposal. One module stays;
  package boundaries enforce import discipline.
- Not breaking compatibility. Migrations carry forward; the
  generic shapes are aliases for the existing types until callers
  swap over.

## Implementation phases (ordered)

1. **Audit** — list every place a daemon imports an arizuko-specific
   type from `core/`, `store/`, `chanlib/`. Output: a dead-clear
   "what would break" inventory per daemon.
2. **Phase A — generic types** — introduce generic shapes in
   `core/`; arizuko-specific aliases in `arizuko/domain.go`. No
   behaviour change.
3. **Phase B.1 — scoped tables** — each daemon owns its tables and
   migrations within `messages.db`. gated stops migrating others'
   tables.
4. **Phase C — gated split** — extract `routerd` and
   `agent-runnerd`. Keep deployable as a single binary
   (`arizuko-monolith`) for ops simplicity during cutover.
5. **Federated API** — at this point [R-platform-api.md](R-platform-api.md)
   ships against generic types and per-daemon ownership. The
   contract is honest.
6. **Phase B.2 — separate DBs** — if/when isolation across hosts
   becomes a real need.

## Open

- Naming: `tenant_id` vs. `namespace` vs. `scope`? Pick once,
  propagate everywhere.
- Capability tokens vs. tier-int: tokens are richer but require
  every daemon to verify scope strings. Tier was a fast int compare.
  Cost analysis on the verification path.
- Channel adapters: do they become "edge daemons" that connect to
  routerd via `/v1/events`, or do they stay tightly coupled to the
  router?
- crackbox vs. arizuko: `crackbox/` is already a sibling component
  with its own discipline (`specs/9/b-orthogonal-components.md`).
  The genericization here aims at the same shape.

## Code pointers

- `core/types.go` — current shared types; will grow generic shapes.
- `core/grants.go` — current rule evaluator; refactor to operate on
  scope lists, not folder/tier directly.
- `chanlib/chanlib.go` — the cleanest current "generic message"
  abstraction; close to the target shape.
- `gated/main.go` — entry point for the future split.
