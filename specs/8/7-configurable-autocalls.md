---
status: draft
depends: specs/5/31-autocalls.md, specs/5/36-yaml-manifests.md, specs/5/5-uniform-mcp-rest.md
---

# specs/7/7 — Configurable autocalls

> Make the `<autocalls>` block operator-extensible. Today five facts
> are hardcoded in `gateway/autocalls.go`. This spec lets operators
> DEFINE additional autocalls — facts injected into every prompt so
> the agent "sees the world around it" (unread counts, sibling
> groups, active topics, custom environment facts) without spending a
> tool call — as a DB-backed, resreg-managed resource: one hand-rolled
> handler reachable via REST, MCP, YAML manifest, and OpenAPI.

## Problem

`specs/5/31-autocalls.md` shipped a fixed registry: five pure
functions (`now`, `instance`, `folder`, `tier`, `session`) in
`gateway/autocalls.go:26`, each `func(AutocallCtx) string`, rendered
into `<autocalls>` by `renderAutocalls` (`gateway/autocalls.go:51`)
and prepended to the prompt at `gateway/gateway.go:1024` — the single
shared sink for both chat and web-topic paths. Adding a sixth fact is
a code edit + rebuild + redeploy. 31's own "planned extension"
(`unread` per-JID, `errors` count) is stuck behind that wall.

What 5/31 cannot do:

- An operator cannot inject "you have 3 unread in solo/inbox" or
  "siblings: eng, sre, oncall" without a gateway code change.
- A deployment cannot carry its autocall set in its config dump
  (5/36 manifest) — the facts an agent sees aren't operator data,
  they're frozen in the binary.
- The planned `unread`/`errors` entries imply cheap store reads are
  acceptable, but 5/31's `AutocallCtx` contract is "no I/O, no
  locks" — there's no sanctioned path for a fact that needs one
  indexed read.

This is the natural evolution of 5/31's heuristic ("inject the fact
when schema-cost > content-cost") and of phase-7's thesis: **agent is
data**. What the agent passively sees each turn becomes operator
config — versioned, gated, projected into the prompt — not a
hardcoded constant.

## Decided model

Three decisions, each resolving a tension from 5/31.

### 1. Two classes of autocall — pure and probe-backed

5/31's `AutocallCtx`/`renderAutocalls` are defined "synchronously in
microseconds — no I/O, no locks" (`gateway/autocalls.go:11`). Adding
a fact that reads the store is **not** a compatible extension of that
contract — it is a deliberate semantic split. This spec REDEFINES the
autocall contract into two classes:

- **Pure autocalls** (the five builtins + configured `template`
  kind): the old contract verbatim. Microseconds, zero I/O, no locks.
- **Probe-backed autocalls** (configured `query` kind): permitted ONE
  deadline-bounded, fail-open-to-omitted store read.

The "no I/O" rule is not relaxed for the pure class; the probe class
is a new, explicitly-bounded contract that lives alongside it. Code
change: `AutocallCtx`'s doc comment is rewritten to describe the two
classes, and `renderAutocalls` gains the budgeted probe loop (below).

The two kinds, each with a hard cost ceiling:

| Kind       | Cost                       | I/O            | Backed by                                |
| ---------- | -------------------------- | -------------- | ---------------------------------------- |
| `template` | microseconds (string ops)  | none           | already-resolved `AutocallCtx` vars      |
| `query`    | one indexed read, budgeted | one store read | a whitelisted, parameterized store probe |

- **`template`** — a small substitution string over the existing
  `AutocallCtx` fields (`instance`, `folder`, `tier`, `session`,
  `now`). Pure, zero I/O, identical cost profile to the five
  builtins. Example expr: `"tier-{tier} agent in {folder}"`. The
  substitution vocabulary is exactly the `AutocallCtx` field set —
  no new resolvers, no arbitrary expression language.

- **`query`** — names ONE entry from a **whitelisted probe table**
  (below) plus its bound arguments. The probe is a Go function in
  the gateway doing a single indexed `SELECT` (or `COUNT`) scoped to
  the resolving folder. NOT arbitrary SQL. NOT arbitrary code. The
  config row picks a probe by name and supplies its parameters; it
  never carries SQL text.

There is no third "run a shell command / call a URL" kind. That is a
function (`specs/7/6-functions.md`) or an MCP tool, not an autocall —
an autocall renders inline on the hot prompt path and must stay
cheap.

### 2. Cost budget — strict, not magical

Probe-backed autocalls render **serially, on the prompt-build
goroutine** — not fanned out. SQLite (WAL, one writer, shared handle)
gives no parallelism benefit for reads on the hot path, and serial
execution makes the total budget a simple running sum instead of a
coordination problem. Three controls:

- **Per-`query` budget.** Each `query` autocall runs under a context
  deadline (`AUTOCALL_QUERY_BUDGET_MS`, default 5ms). The probe
  receives a `context.Context` carrying that deadline and MUST honor
  it (`QueryContext`); SQLite cancels the statement on deadline. On
  timeout OR error the entry is **omitted** (see §Omission
  semantics).
- **Total prompt-build budget.** The serial loop tracks elapsed
  wall-clock against `AUTOCALL_TOTAL_BUDGET_MS` (default 20ms). Once
  exceeded, remaining `query` entries are skipped without running.
  Pure entries (builtins + `template`) always render — they're free.
- **Concurrency cap.** The renderer is the only autocall caller and
  runs once per prompt build; there is no fan-out. Under writer-lock
  contention the per-`query` deadline bounds the wait — a probe that
  can't get its read in 5ms is omitted, not queued. The total budget
  caps worst-case added prompt-build latency at
  `AUTOCALL_TOTAL_BUDGET_MS` regardless of how many `query` rows a
  scope defines.

Skips are logged at `slog.Warn` (`autocall_skipped`, fields: `name`,
`scope`, `reason ∈ {timeout, error, budget}`) so an operator who
wrote a slow autocall sees it in journald. To avoid per-turn log
spam, the gateway logs a given `(name, scope, reason)` at most once
per `AUTOCALL_LOG_INTERVAL` (default 60s); the value is never logged.

This is the explicit resolution of tension #1: configurability does
NOT break the pure-autocall no-I/O posture, and the probe class is
bounded — every probe-backed read is deadline-bounded,
serially-budgeted, and fail-open-to-omitted.

### 3. DB-backed, resreg-managed resource

The autocall definitions are **business state**, so they live in the
DB (CLAUDE.md: "business state is DB-backed; infra is env"). The
_budgets_ (`AUTOCALL_*_MS`) are infra and stay env vars.

The definitions are one `resreg.Resource` (`autocalls`), owned by
`gated`, hand-rolled like every other resource (CLAUDE.md:
"MCP + REST hand-rolled and uniform … one hand-written handler").
The win is uniformity, not codegen: the resource declares one
`RowType` + `Handler` + `Authz`, and because it sets
`RowType`/`Table`/`PKFields`/`Scope`, the 5/36 engine drives the
mechanical CRUD/scan/parse/emit off that struct. From that one
declaration the platform's existing surfaces reach it:

- REST CRUD (`/v1/autocalls`, scoped paths under a group),
- MCP tools (`autocalls.create`, `.list`, `.delete`, …),
- YAML manifest round-trip (`arizuko export`/`apply`),
- `/openapi.json` schema (5/36 §"OpenAPI emission"),
- one audit row per mutation, ACL-gated by `autocalls:<action>`.

The per-resource cost is the same as any 5/36 resource — a struct, a
`Register` call, and a `ValidateRow` hook (below); not zero, but
small and uniform. The `RowType` struct is the single contract; all
surfaces decode into it. No second config path, no env-var fallback,
no file format invented.

## Schema

One table, one resreg `Row` struct. Folder-scoped via
`resreg.ScopeSpec{Field: "Scope"}`.

```
CREATE TABLE autocalls (
  scope      TEXT NOT NULL,            -- '' = instance-wide, else folder path
  name       TEXT NOT NULL,            -- rendered label; key within scope
  kind       TEXT NOT NULL,            -- 'template' | 'query'
  expr       TEXT NOT NULL,            -- template string OR probe name
  params     TEXT NOT NULL DEFAULT '', -- JSON object of probe args (query only)
  seq        INTEGER NOT NULL DEFAULT 0, -- render order within scope
  enabled    INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (scope, name)
);
CREATE INDEX autocalls_scope ON autocalls(scope, enabled, seq);
```

| Column    | Meaning                                                                                    |
| --------- | ------------------------------------------------------------------------------------------ |
| `scope`   | `''` = instance-wide default; a folder path = that folder ONLY (no inheritance)            |
| `name`    | the label rendered as `name: value`; unique within scope; builtin names reserved (§Render) |
| `kind`    | `template` or `query`; rejected at validate-time if other                                  |
| `expr`    | `template`: substitution string. `query`: a probe name from the whitelist                  |
| `params`  | `query` only: JSON args bound to the probe (e.g. `{"window":"24h"}`); else `''`            |
| `seq`     | render order; ties broken by `name` lexicographically                                      |
| `enabled` | `0` rows parse + export but never render — toggle without delete                           |

resreg `Row` (Go, illustrative — implemented when this ships):

```go
type Row struct {
    Scope   string `db:"scope"   yaml:"-"       json:"scope"   pk:"true"`
    Name    string `db:"name"    yaml:"name"    json:"name"    pk:"true"`
    Kind    string `db:"kind"    yaml:"kind"    json:"kind"`
    Expr    string `db:"expr"    yaml:"expr"    json:"expr"`
    Params  string `db:"params"  yaml:"params"  json:"params"`
    Seq     int    `db:"seq"     yaml:"seq"      json:"seq"`
    Enabled bool   `db:"enabled" yaml:"enabled"  json:"enabled"`
}
```

`Scope` is `yaml:"-"`: in a manifest it's set from the YAML position,
not a row field, exactly as `acl`/`scheduled_tasks` derive their
folder in 5/36. Two positions:

```yaml
# base.yaml — instance-wide rows: top-level resource key, scope = ''
autocalls:
  - name: env
    kind: template
    expr: 'staging instance'
    seq: 0

# atlas.yaml — folder rows: nested under the group key, scope = 'atlas'
atlas:
  autocalls:
    - name: unread
      kind: query
      expr: unread
      seq: 0
    - name: siblings
      kind: query
      expr: siblings
      seq: 1
```

The parser maps the instance-wide list to `scope=''` rows and the
nested list to `scope=<folder>` rows. PK is `(scope, name)`, so the
5/36 merge rules apply unchanged: same `(scope, name)` with identical
payload across files dedups; with differing payload it's a parse-time
error; twice in one file is a parse-time error.

`ValidateRow` enforces: `kind ∈ {template, query}`; `name` is not a
reserved builtin name (`now`, `instance`, `folder`, `tier`,
`session`) — reserved names reject (see §Render, "Builtin names are
reserved"); for `query`, `expr` names a registered probe and `params`
decodes to that probe's typed arg schema (unknown keys reject); for
`template`, `params` is empty and `expr` references only known
`AutocallCtx` vars (`{instance}`, `{folder}`, `{tier}`, `{session}`,
`{now}`).

**`params` is a typed hole, acknowledged.** It's a JSON `TEXT` column
so the row schema stays one flat struct (the 5/36 engine handles
scalar columns, not per-probe variant shapes). Its _contents_ are not
free-form: `ValidateRow` decodes `params` against the named probe's
declared arg schema and rejects unknown/mistyped keys. The OpenAPI
schema documents `params` as an object whose shape depends on `expr`;
this is the one place autocalls trade strict column typing for the
whitelist's per-probe validation. The trade is contained because the
probe set is closed and platform-owned.

### Probe whitelist

`query` autocalls may only name a probe from a small in-gateway
registry. Each probe is a NEW Go func written for this purpose with a
declared, typed arg schema and a **folder-scoped, cardinality-bounded
read**. The existing store methods below are _models_, not
drop-in backings — several are global or web-only today and must be
written/adapted to the probe contract:

| Probe      | Args     | Read (must be added/adapted)                                                                                         | Bound               | Renders e.g.           |
| ---------- | -------- | -------------------------------------------------------------------------------------------------------------------- | ------------------- | ---------------------- |
| `unread`   | —        | messages-since-agent-cursor for this folder's chat JID(s)                                                            | COUNT, 1 row        | `unread: 3`            |
| `errors`   | —        | errored-chat count **scoped to folder** (today `CountErroredChats` is global — needs a `WHERE folder=?` variant)     | COUNT, 1 row        | `errors: 1`            |
| `siblings` | —        | `SiblingFolders(folder)` (already folder-scoped, `open=1`)                                                           | ≤ N names           | `siblings: eng, sre`   |
| `topics`   | `limit?` | active topics for folder (today `Topics` is web-JID-only, `LIMIT 100` — needs scoping + a small `limit`)             | ≤ limit (default 5) | `topics: eng-sync, q3` |
| `tasks`    | —        | active scheduled-task count **for folder** (today `CountActiveTasks` is global — needs `WHERE chat_jid LIKE folder`) | COUNT, 1 row        | `tasks: 1 active`      |

Each probe declares, in code:

1. **Typed args** (name + type), so the row's `params` validate
   against a schema, not free-form.
2. **A single SQL statement** whose WHERE filters on the resolving
   folder (or its JID derivation) and whose plan is index-supported —
   asserted by an `EXPLAIN QUERY PLAN` test (no `SCAN TABLE` of
   `messages`).
3. **A result cardinality cap** — either an aggregate (one row) or an
   explicit `LIMIT`. A probe that can return unbounded rows is not
   admissible; the cap is part of the probe's definition, not the
   config.

The whitelist grows by a gateway code change (one Go func + one
registry line) — deliberately. A probe is a hot-path read on every
turn for every group that enables it; vetting it (folder-scoped,
index-supported, cardinality-capped) is a platform decision, not
operator config. Operators COMPOSE probes via rows; they don't author
probe bodies. This keeps tension #1 closed: there is no path from
config to unbounded work.

**Cross-folder metadata is a platform-vetted exception, not a
default.** `siblings` surfaces _names_ of adjacent `open=1` folders —
metadata, never content. That is admissible because (a) it's the same
information a tier-appropriate agent could already enumerate, and (b)
it's restricted to open siblings (closed folders never appear). A
probe that would surface another folder's _content_ is NOT admissible
under this spec — that's an observe/`group_watchers` concern with its
own ACL gate. The probe author must justify any cross-folder read at
whitelist-review time; self-folder reads are the norm.

## Scope and inheritance

- **Two scopes only:** instance-wide (`scope=''`) and per-folder
  (`scope=<folder path>`).
- **No parent-folder inheritance.** A row at `corp/eng` does NOT
  apply to `corp/eng/sre`. CLAUDE.md is explicit: "no parent-folder
  inheritance for group-scoped files." An autocall is group-scoped
  config; same rule. If `corp/eng/sre` wants the fact, it gets its
  own row (or relies on the instance-wide default).
- **Merge:** for a given prompt the rendered set is `builtins ∪
instance-wide rows ∪ this-folder rows`. Exactly two config layers,
  no chain to walk.

This deliberately differs from how some platforms do hierarchical
config; the cost of a silent inheritance chain (an agent three levels
deep seeing a fact nobody scoped to it) is exactly the "magical"
behavior CLAUDE.md forbids. Explicit per-folder rows + an
instance-wide default cover the real cases.

## Ownership and safety

A configured `query` autocall runs server-side on the hot prompt-build
path for its scope's group on EVERY turn. A slow or expensive one
degrades that group's latency. Bounding (§Decided model #2) caps the
blast radius to the budget; ownership caps who can arm it.

- **Operator-owned by default.** `autocalls:create` / `:update` /
  `:delete` are ACL actions (`specs/4/9-acl-unified.md`); the seed
  grant scopes them to operators (the `**` grant row). An ordinary
  group member cannot define an autocall.
- **Per-folder delegation is a grant, not a default.** An operator
  MAY grant `autocalls:create` scoped to a folder (e.g. let a team
  lead define facts for `corp/eng/`). That is an explicit ACL row,
  audited like any other — not implicit because you're a group
  member.
- **The whitelist is the real safety boundary.** Even a delegated
  folder owner can only compose existing probes; they cannot
  introduce SQL, code, or a network call. The worst a malicious row
  can do is name a whitelisted probe with odd params and a confusing
  label — bounded, fail-open-to-omitted, and visible in the audit
  log + the rendered block itself.
- Budgets (§2) protect the platform from an _accidentally_ expensive
  config; ACL + whitelist protect it from a _malicious_ one.

## Render and merge with builtins

One renderer, one sink — unchanged from 5/31. `renderAutocalls`
(`gateway/autocalls.go:51`) stays the only producer of the
`<autocalls>` block; `g.autocallsBlock(folder, topic)`
(`gateway/gateway.go:1024`) stays the only call site. This spec
widens what that one renderer iterates, it does not add a second
path. (CLAUDE.md: "one renderer, many sinks.")

Order within the block:

1. **Builtins first**, in their existing fixed order (`now`,
   `instance`, `folder`, `tier`, `session`). They keep being defined
   in code, not seeded as rows — they are platform invariants, not
   operator config, and code is their natural home (zero migration,
   zero rebuild risk).
2. **Instance-wide configured rows**, by `(seq, name)`.
3. **This-folder configured rows**, by `(seq, name)`.

Empty values are dropped (as today): a `template` resolving to `""`
or a `query` that omitted (see §Omission semantics) produces no line.

**Builtin names are reserved.** The five builtin names (`now`,
`instance`, `folder`, `tier`, `session`) cannot be used by a
configured row — `ValidateRow` REJECTS such a row. No shadowing: an
operator who wants a richer session fact picks a different name
(`session_full`). This keeps the builtins' "ground truth" meaning
fixed — a `tier:` line always means the platform-resolved tier, never
an operator's reinterpretation — and removes a footgun (silently
overriding `now` would let a misconfigured row hide the real clock).

**Configured-name collisions across the two config layers.** A
`(folder)` row and an `(instance-wide)` row may share a `name`; the
folder row wins and the instance-wide one is dropped — last-writer
folder-beats-instance, so the block never has two `unread:` lines.
This collision is between configured rows only; builtins are never in
the contest (their names are reserved out).

**Determinism under timeout.** Collision resolution runs on the
DEFINITIONS (by `(scope, name, seq)`), before any probe executes —
the set of rows that _would_ render is fixed and deterministic
regardless of which probes later time out. A timed-out/errored probe
drops its own line (§Omission) but never promotes the row it
shadowed: a shadowed row is already gone at definition-merge time, so
the block's _line set_ depends only on config, while _line presence_
depends on probe outcome. This makes the rendered block reproducible
given (config, store state) and turn-to-turn changes only as the
underlying counts change — not as a function of scheduling.

The agent contract is unchanged (`ant/CLAUDE.md` §Autocalls): treat
the block as ground truth, do NOT call a tool to re-fetch. Configured
facts are indistinguishable from builtins at the agent's eye — that's
the point.

## Omission semantics

A `query` autocall that produces no line collapses four distinct
causes into "absent." The rule (and its honest limit):

| Cause           | Line rendered? | Meaning to the agent |
| --------------- | -------------- | -------------------- |
| true zero/empty | yes — `x: 0`   | the fact, measured   |
| no rows (list)  | no line        | nothing to report    |
| timeout         | no line        | unknown this turn    |
| probe/DB error  | no line        | unknown this turn    |

**Count probes render `0`, not absence.** `unread: 0` is a fact and
MUST render — collapsing it to a missing line would let the agent
mistake "measured zero" for "couldn't measure." Only list/`siblings`
probes with genuinely empty results omit their line (a `siblings:`
with no value is noise).

**Failure (timeout/error) always omits.** The agent cannot
distinguish "measured zero" from "failed to measure" for a probe that
failed — that is the deliberate strictness cost: a present line is
always a real measurement, never a guess or a stale value. The
operator-side signal for failures is the `autocall_skipped` slog line
(rate-limited, §2), NOT a degraded prompt. We accept that the agent
loses the failure signal in exchange for never feeding it a fabricated
or stale fact (CLAUDE.md: "strict, not magical").

## Migration and compatibility

- **Builtins stay in code.** No seed rows for the five. A deployment
  that configures nothing renders byte-identical output to today —
  zero behavior change. (The "one renderer" change is internal: the
  loop gains a "then append configured rows" step that is a no-op on
  an empty table.)
- **Test invariants update with the renderer.** `renderAutocalls`
  changes signature (it now needs store + folder + budgets, not just
  `AutocallCtx`), so `gateway/autocalls_test.go` and any prompt-build
  snapshot tests must be updated in the same commit: an empty-table
  case asserting byte-identical legacy output, plus new cases for
  `template` render, `query` render/omit/timeout, reserved-name
  reject, and folder-beats-instance collision (ship-with-tests:
  MEMORY.md).
- **Table is additive.** One `store/migrations/NNN` adds the
  `autocalls` table + index; gated owns it (memory: gated owns
  schema). No existing table touched.
- **Per the ship checklist** (CLAUDE.md §Shipping changes): CHANGELOG
  entry, migration stub under `ant/skills/self/migrations/`,
  `MIGRATION_VERSION` bump, agent image rebuild. The `ant/CLAUDE.md`
  §Autocalls paragraph gains one sentence: "operators may define
  additional autocalls; they appear in the same block and are equally
  authoritative."
- **resreg adoption** rides 5/36's engine: `autocalls` is one more
  `resreg.Resource` with `RowType`/`Table`/`PKFields`/`Scope` set —
  it gets CRUD/YAML/OpenAPI from the engine. The only non-generic
  code is the gateway-side renderer integration and the probe
  registry (neither is a transport concern).

## What this is NOT

- **NOT arbitrary code or SQL from config.** No expr language beyond
  `AutocallCtx` substitution; no SQL text in rows; `query` autocalls
  name a vetted, parameterized probe. Injection surface is zero;
  cost is bounded by the probe being a single indexed read.
- **NOT parent-folder inheritance.** Instance-wide default + explicit
  per-folder rows. No chain walk. (CLAUDE.md.)
- **NOT a blocking dependency on data.** Every `query` is
  deadline-bounded and fail-open-to-omitted. A down/slow probe never
  blocks or fails a turn.
- **NOT a replacement for MCP tools or functions.** Anything needing
  args from the agent, side effects, on-demand timing, network, or
  shell is an MCP tool or a function (`7/6`), not an autocall. The
  autocall niche is unchanged from 5/31: always-relevant, small,
  read-only facts injected without schema cost.
- **NOT an env-var or file config.** Definitions are business state →
  DB-backed via resreg. Only the budgets are env (`AUTOCALL_*_MS`).
- **NOT a new render path.** One renderer, one sink; configured rows
  extend the loop, they don't fork it.

## Related

- `specs/5/31-autocalls.md` — the shipped hardcoded registry this
  generalizes; its planned `unread`/`errors` entries become the first
  two probes here.
- `specs/5/36-yaml-manifests.md` — the DB-backed resreg substrate;
  `autocalls` is one more catalog resource, manifest-addressable,
  scoped by group nesting (`scope` implied like `acl`).
- `specs/5/5-uniform-mcp-rest.md` — `resreg.Resource` wires one
  hand-rolled handler to REST + MCP + audit + the ACL gate;
  `autocalls:<action>` is the action key.
- `specs/4/9-acl-unified.md` — `autocalls:*` actions; operator-owned
  by default, folder-delegable by explicit grant.
- `specs/7/6-functions.md` — the escape hatch for facts that need
  real work (shell, network, unbounded); autocalls stay cheap and
  inline, functions do the heavy lifting elsewhere.
- `ant/CLAUDE.md` §Autocalls — agent-side contract (ground truth,
  don't re-fetch); unchanged in spirit, one sentence added.
- Code anchors: `gateway/autocalls.go:26` (builtin slice), `:51`
  (`renderAutocalls`), `gateway/gateway.go:1024` (single sink),
  `resreg/engine.go` (`ScopeSpec`, `Hooks`, generic CRUD).

## Open questions

1. **Cross-folder probes.** `siblings` reads names of sibling
   folders — benign (names only). Should any probe ever surface
   _content_ from another folder? Lean no: an autocall renders for
   this group's agent; cross-folder content is a routing/observe
   concern (`group_watchers`), not a passive fact. Keep probes
   self-folder-scoped unless a concrete need says otherwise.
2. **Per-probe cache.** `siblings`/`topics` change rarely but render
   every turn. A short TTL cache keyed `(probe, folder, params)`
   could cut even the budgeted read. Deferred: 5/36's no-config-cache
   rule applies to config rows, not to a probe _result_ cache, but a
   stale-read window needs its own justification. Wait for a measured
   hot path before adding.
3. **Budget defaults.** 5ms/20ms are guesses; tune against real
   prompt-build timing once the first probes ship. They're env, so
   tuning is a config change, not a code change.
