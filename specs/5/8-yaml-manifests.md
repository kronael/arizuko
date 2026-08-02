---
status: partial
shipped: 2026-06-14
depends: specs/5/17-openapi-mcp.md, specs/9/2-data-model.md
---

# specs/5/8 — YAML manifests: transport dump/import for cold-tier config

> **DECISION.** The SQLite DB is authoritative. YAML manifests are a
> transport dump/import — `pg_dump` / `pg_restore` for the cold tier — not
> a continuously-synced source of truth. No DB→YAML sync, no startup-apply,
> no SIGHUP-reload. A dump never claims to be live, so "drift" is a
> non-concept. `specs/9/3-git-as-truth.md`'s continuously-synced
> cold-tier-config is superseded; committing an `export` dump to git is fine
> (9/3 itself is unedited — read its `agents.toml` references through this
> lens).

## Status: engine shipped, CLI inert in production

The resreg engine and the YAML format ship (`resreg/engine.go`,
`resreg/resources/*.go`, 13 registered resources). The CLI does **not**
reach a production instance: `arizuko apply`/`plan`/`export`/`get` all call
`store.Open(dataDir+"/store")` (`cmd/arizuko/apply.go:45,94,128,204`),
which opens the frozen pre-split `messages.db` (`store/store.go:51`) — not
the `routd.db` / `onbod.db` the daemons actually read. Verified
2026-08-02. Finalizing = repoint the CLI per-resource to its owner DB per
[`5/16`](16-mcp-rest-unification.md)'s owner-DB map. Tracked in `BUGS.md`.

Read every "per owner DB" statement below as the **decided target**, not
shipped behaviour: today exactly one `config_meta` row exists, in
`messages.db` (`store/migrations/0067-config-meta.sql`), and
`resreg.Apply` takes a single `*sql.DB` (`resreg/engine.go:490`).

## Why

`9/2`'s cold/warm/hot boundary left `agents.toml` unspecified. This spec
replaces it with a YAML manifest carrying an instance's cold-tier config:
ACL, routes, secrets metadata, scheduled tasks, proxyd routes, web routes,
network rules, group registration.

## Surface

Four verbs, all in `cmd/arizuko/apply.go`:

- `arizuko export` — dump all cold-tier config tables to YAML.
- `arizuko apply <file>…` — restore: validate, then rebuild config tables.
- `arizuko get <resource>[/<name>]` — a scoped `export` of live state.
- `arizuko plan <file>…` — non-mutating diff vs live state.

**Rebuild scope is the load-bearing decision.** Per resource, DELETE+INSERT
is scoped to the folders the manifest mentions (`DELETE … WHERE folder IN
(<manifest scope>)`). A row's omission deletes it **only within a mentioned
scope**; groups absent from the manifest are untouched. Instance-global
resources rebuild wholesale. This is why `--prune` / `state: absent` were
cut — absence within a named scope already means "remove".

Manifest files compose additively: `apply manifest/` reads every `*.yaml`,
merges rows by PK, applies the union. **File names are informational** —
content, not name, decides what a file holds. Duplicate PKs across files
with differing payloads are a parse-time error; identical payloads
deduplicate. No `include:` directives — flat composition only, so merging
is associative and errors reproducible.

Document schema: group folder is the top-level key with owned resources
nested flat beneath it; instance-global resources (`proxyd_routes`,
`onboarding_gates`, `network_rules`) are top-level resource-kind keys with
no group wrapper. There are **no daemon section keys** — the apply tool
resolves each resource name to its owning daemon at dispatch, so a future
daemon split leaves manifests valid.

## Three transports, one row schema

REST, MCP, and YAML are three transports over the **same row schema**,
defined once as `resreg.Resource` and reused by all three. Only the **row
fields** belong to the resource; verb, identity, batching, and version are
transport envelopes. YAML's envelope is: DROP+INSERT for the verb, YAML
nesting for identity, many-rows-one-tx for batching, `config_version:` for
CAS.

`5/17` owns the transport half (`Name`, `Endpoints`, `MCPDoc`, `Gate`,
`Handler`, `Store`). **This spec is authoritative for the row-schema half**
the engine adds — `RowType`, `Table`, `PKFields`, `Scope`, `Hooks`,
`SkipApplyRebuild` (`resreg/resreg.go:204`).

`RowType` is what makes "one schema" more than a claim: all three
transports decode into instances of it, so adding a column edits one struct
and drift becomes a compile error rather than a silent divergence.

**The engine handles shape, not semantics.** Reflection over `db:`/`yaml:`/
`json:` tags generates SELECT/INSERT/scoped-DELETE/parse/emit for the
strict subset (scalar columns, no NULLs, declared PK, RFC3339 timestamps).
Everything outside it — validation beyond types, derived fields, secret
encryption, cross-row constraints, delete cleanup — goes in per-resource
`Hooks`, the **only** per-resource code. Engine surface:
`resreg/engine.go:170-381`.

`Scope` cannot be inferred from `RowType`: `acl` and `acl_membership` have
no folder column (scope lives in an `acl.scope` glob / membership edges),
and `routes.target` carries `#observe`/`#topic` fragments so it is not
column-equal to a folder. The engine reads `Resource.Scope`, never guesses.

## OpenAPI emission

**This spec owns OpenAPI emission** (it subsumed the former
openapi-discoverable spec). The same `RowType` reflection emits an OpenAPI
3.1 document per daemon — no `huma`, no `swag`, no codegen
(`resreg/openapi.go`). Every HTTP-serving daemon mounts `GET
/openapi.json`, including daemons owning no resources, so the aggregator
page (`/pub/arizuko/reference/openapi.html`) lists them uniformly.

**The endpoint is public and mounts BEFORE auth middleware** — schemas
describe surface, not data. The doc is cached for the process lifetime.

Known limit: OpenAPI emits a fixed CRUD convention, so a resource whose
mounted handler diverges from its `resreg/resources/*.go` declaration can
still drift. Single-sourcing the two declarations is `5/16`'s deferred
"one owner + federation" work.

## Optimistic locking (`config_version`)

**Every writer ADVANCES the version; only YAML apply CHECKS it (CAS).**
MCP/REST single-row mutations bump the counter in their own tx so a later
apply detects them, but they carry no version to compare — they are
single-row, write-and-read in one call, serialized by `BEGIN IMMEDIATE`,
with no stale snapshot to defend. YAML apply **is** the stale-snapshot
pattern (export at T, edit for hours, apply at T+N), so it is the one
writer that CAS-checks before writing and rejects on mismatch. Without it,
two operators both exporting at v42 would silently clobber each other.

`--force` skips the check (last-writer-wins; still advances).

Three implementation rules, all load-bearing:

- **Bump once per writer tx, at the commit site — never an AFTER trigger.**
  A per-row trigger would advance by N on a bulk apply of N rows, breaking
  equality CAS. The invariant is monotonicity, not consecutive integers.
- **`BEGIN IMMEDIATE`** takes the RESERVED lock at tx start so concurrent
  applies serialize; WAL readers are unaffected. Without it two applies
  could both pass CAS at v42 and the second would wipe the first.
- **The bootstrap counts existing config rows**, so a fresh operator does
  not need `--force` on first apply against a populated DB.

`secrets` is excluded from the counted set: routine blob rotation must not
invalidate every pending apply.

## Atomicity model

**The atomic unit is the resource, not the manifest.** Each resource is
owned by exactly one DB, and `apply` runs one `BEGIN IMMEDIATE … DELETE
scoped; INSERT; COMMIT` **per owner DB**. Within one owner DB the rebuild
is atomic. There is **no global all-or-nothing across DBs** — SQLite cannot
open one tx across separate files.

A partial outcome (one DB committed, another failed) is recovered by
**re-running the same apply**: the rebuild is declarative and keyed on PK,
so a second run converges — committed DBs re-apply to a no-op, the failed
one retries. `apply` stays ONE operator command; the fan-out is internal.

Idempotence rests on three guardrails: no side-effecting apply hooks (hooks
are pure normalization/validation); each row carries its PK and all fields;
secrets are metadata-only so a re-run never wipes a rotated blob.

## Apply is a restore, so filesystem prep follows the commit

Group filesystem state (skills, `.claude/`, prototype) is **eventually
consistent with the DB** — filesystem ops cannot join a SQLite tx. After
the config tx commits, `apply` calls `container.SetupGroup(folder)` for
every group row lacking a complete on-disk dir. A failed `SetupGroup`
surfaces as an apply error, never swallowed: a row without its dir makes
routing `docker run` against a missing path and exit 125. `arizuko repair`
re-runs the prep alone, idempotently. Direct `mkdir` of a group is
forbidden (CLAUDE.md).

Removing a group from the manifest deletes its row but **not** its
directory; `arizuko group purge <folder>` does full removal.

## Group removal semantics

**DECISION: when apply removes a `groups` row, active routing state is
cleared in the same tx; runtime history is not.**

Cleared in the DELETE tx — live refs that would silently misroute if left
dangling: `chats.sticky_group`, `chat_reply_state.engaged_folder`,
`group_watchers` (either side), `router_state` cached pointers.

Left intact, keeping the orphaned folder string for forensics:
`messages`, `audit_log`, `cost_log`, `secret_use_log`, `task_run_logs`.

Full erasure is `arizuko group purge <folder>` — intentionally imperative
and destructive in a way YAML apply is not. `plan` warns on group removal,
naming the routing rows cleared and the history rows stranded.

## Tokens in manifests

**DECISION: a resource whose PK is a system-generated secret must never be
rebuilt from a manifest.** A full rebuild would wipe live tokens; preserving
them would need either secret values in YAML or a name-indirection layer —
both disproportionate. Tokens stay imperative (`arizuko invite …`,
`arizuko token …`); their mutations still bump `config_version` and audit.

<!-- VERIFIED 2026-08-02: the code only half-honours this. `route_tokens`
     sets SkipApplyRebuild (resreg/resources/route_tokens.go:111), but
     `invites` does NOT (resreg/resources/invites.go:36-44) — it carries a
     RowType with no skip, so Apply's loop (resreg/engine.go:523) would
     DELETE+INSERT live invite tokens, and Export emits the raw bearer.
     Inert only because the CLI still targets the frozen messages.db.
     `invites` needs SkipApplyRebuild before any export/apply ships.
     Also tracked as a 5/20 blocker. -->

Deferred: v2 encrypted token export (operator-supplied key, `token:
'enc:AES-GCM:<b64>'`) if demand ever surfaces.

## Config vs runtime tables

Two table classes by **documentation discipline** — no prefixes, no
separate files, just a rule about which class owns which table. Config
tables are operator-authored cold-tier intent; runtime tables are
system-generated record. The canonical membership list is the set of
registered resources (`resreg/resources/*.go`), not a table here.

Rules that must be upheld:

1. `apply` writes only config tables. **One named exception:** removing a
   `groups` row clears that group's routing side-channels in the same tx
   (above).
2. Runtime tables are never bulk-DELETEd — only by explicit retention/purge.
3. Cross-class JOINs are expected (dashd, reporting). The split is a
   write-discipline boundary, not a query boundary.
4. No new table joins the config class without a `resreg.Resource`. A table
   that is not manifest-addressable belongs to the runtime class.
5. **No daemon may cache config-table rows in memory** (normative). One
   indexed read is cheaper than any cache invalidation, and an in-memory
   config cache creates a stale-read window that makes apply semantics
   undefined. The one allowed cache is
   `sync.Map[backendURL]*httputil.ReverseProxy` — it caches connections,
   not config rows; the row that picked the URL is re-read per request.
   (Shipped: proxyd's `routesResource` mutex+snapshot is gone —
   `proxyd/resource.go:5`.)

Restore semantics: all daemons see new config on their next DB read — no
signals, no reload endpoints, no cache invalidation. WAL gives readers
snapshot isolation during the tx.

## Round-trip honesty

`get`/`export` must emit a fragment that re-applies to a no-op — exact
shape `apply` accepts, no extra or omitted fields, no reordering.

**Canonical key order is mandatory** (Go map iteration is
non-deterministic): `config_version` first, then group folders
lexicographically, then global resource keys lexicographically; within a
group, catalog order; within a list, rows sorted by PK. Two consecutive
exports must be byte-identical on an unchanged DB or file hashing and git
diffs break.

## Secret safety

Secret blobs never appear in manifest YAML (metadata only), `plan` output
(shown as set/unset), error payloads, or audit rows. `secrets` carries
`SkipApplyRebuild` (`resreg/resources/secrets.go:54`) so apply validates
and diffs the metadata but never DELETE+INSERTs — a rebuild would wipe the
imperatively-set blob. Setting one is a separate operator command,
`arizuko secret set`. Trust boundary unchanged from
[`9/2 ## secrets`](../9/2-data-model.md#secrets).

Folder-scoped secrets infer the folder from group nesting and declare only
`key:`; user-scoped ones add an explicit `user:` — the same
implicit-from-nesting rule as `acl` and `scheduled_tasks`.

## Markdown vs YAML

**If it's a row, YAML. If it's a paragraph, Markdown.** YAML carries
table-shaped cold-tier rows (operator intent). Markdown carries prose
(`PERSONA.md`, `MEMORY.md`, `.diary/`, `skills/<name>/SKILL.md`,
`PRODUCT.md`) — agent context living in the group directory, never manifest
rows, never referenced from YAML, never content-hashed in the DB.

## Status is not in the manifest

A dump carries cold-tier config rows only; live state is read by `arizuko
get`. Dumps never carry `status:` / `applied_at:` / `last_error:` — the
same spec/status boundary `kubectl` draws.

## FK posture

**FKs are ON globally** (`store/store.go` sets `PRAGMA foreign_keys=ON` per
connection). **Declare a FK when** (a) the reference is row-shaped — single
target table, not polymorphic — and (b) on parent delete the runtime wants
either silent CASCADE or explicit RESTRICT, never silent dangling.

Three FKs qualify in v1, all CASCADE: `task_run_logs → scheduled_tasks`
(0011), `web_routes.folder → groups` (0068), `route_tokens.owner_folder →
groups` (0069). Run history, URL pinning, and webhook tokens all become
meaningless when their parent dies.

Every other cross-table reference is **intentionally string-typed**, for
one of four reasons: polymorphic encoding with no single target table
(`acl.principal`, `acl.scope`, `acl_membership`, `secrets.scope_id`,
`scheduled_tasks.chat_jid`); legitimate rows a FK would reject
(`network_rules.folder=''` carries instance-global rules, and SQLite has no
`FOREIGN KEY … WHERE` to exclude them); not column-equal to a folder
(`routes.target`); or deliberate stranding for forensics (`messages`,
`audit_log`, `cost_log` — CASCADE would delete silently, RESTRICT would
block legitimate removals). Active routing state (`chats.sticky_group`,
`chat_reply_state`, `group_watchers`) is cleared explicitly in the apply tx
instead, so the cleanup is auditable and ordered rather than a silent
SET NULL.

**Insert in catalog order** so `groups` precedes its children; DELETE
reverses. No config-to-config cycle exists in v1; if one appears, set
`PRAGMA defer_foreign_keys=ON` at tx start.

## Non-goals

No live reload / file watcher / DB→YAML sync. No DAG dependency resolution
beyond catalog ordering. No web UI for manifests, no multi-instance apply,
no transactional cross-daemon rollback. Imperative `arizuko group add`
verbs stay for ad-hoc work — manifests are the declarative path, not a
replacement.

Implementation constraint: standard Go idioms only — `reflect`, struct
tags, `database/sql`, `gopkg.in/yaml.v3`, `encoding/json`. No DSLs, no
codegen, no third-party ORMs.

## Open questions

1. **Registry endpoint shape** — `GET /v1/_resources` as JSON Schema vs a
   custom shape. Decide at implementation time.
2. **Cross-class dependencies** — a `scheduled_task` referencing an invite
   landing later needs two apply runs. DAG resolver only if a real
   collision surfaces.
3. **dashd as a manifest editor** — out of scope.

## Cross-refs

- [`17-openapi-mcp.md`](17-openapi-mcp.md) — the transport half of
  `resreg.Resource`; the REST + MCP faces this tool talks to.
- [`16-mcp-rest-unification.md`](16-mcp-rest-unification.md) — the owner-DB
  map the CLI must be repointed to.
- [`../9/2-data-model.md`](../9/2-data-model.md) — cold/warm/hot tier
  boundary; this spec touches cold tier only.
- `../9/3-git-as-truth.md` — **reframed, not
  adopted** (see lead DECISION); unedited.
- `../9/4-data-ingestion-curation-eventing.md`
  — Q2/Q5 open; extend the resource set when they resolve.
- [`5-tenant-self-service.md`](5-tenant-self-service.md) — Phase C secret
  layering composes with the `secrets` resource.

## Pointers

- Engine: `resreg/engine.go`, `resreg/openapi.go`, `resreg/README.md`
- Resource declarations: `resreg/resources/*.go`
- CLI: `cmd/arizuko/apply.go`
