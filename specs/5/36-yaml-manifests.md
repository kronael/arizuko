---
status: draft
depends: specs/5/5-uniform-mcp-rest.md, specs/7/2-data-model.md, specs/7/3-git-as-truth.md
---

# specs/5/36 — YAML manifests: declarative carrier for cold-tier intent

## Why

7/2 sharpens the cold/warm/hot tier boundary; 7/3 puts cold-tier
config in git; both leave a placeholder string — `agents.toml` —
without specifying the file shape. This spec resolves it.

`agents.toml` was always provisional. This spec replaces it with a
YAML manifest format that **carries cold-tier intent** for an
instance: ACL, routes, secrets metadata, scheduled tasks, invites,
proxyd routes, web routes, network rules, group registration —
every cold table covered by `resreg.Resource` per
[`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md). Prose
artifacts (PERSONA.md, MEMORY.md, .diary/) stay as Markdown files,
referenced from YAML by path; the YAML never inlines their bodies.

The mechanism is mechanical: one CLI verb (`arizuko apply`) parses
YAML, validates each row against the live resreg registry, and
dispatches REST calls. resreg's tx-bound audit fires per row. No
new write path, no new auth gate, no daemon-side state machine.

## What this spec is

The **carrier format** for cold-tier configuration and the **apply
loop** that drives it through resreg. It is intentionally narrow:
this is the YAML shape and the dispatch rules, nothing more.
Product composition, cross-product subscriptions, and ingestion
semantics ([`7/4`](4-data-ingestion-curation-eventing.md) Q2 + Q5)
remain open — 7/5 gives them a place to land later, not an answer.

## Surface

Three verbs, mirroring `kubectl`:

- `arizuko apply <file>…` — read manifest(s), validate, dispatch
  per-row resreg calls, report results.
- `arizuko plan <file>…` — same as apply but **non-mutating**;
  prints the diff vs live state. No daemon-side writes, no audit
  rows.
- `arizuko get <resource>[/<name>]` — dump live state as a manifest
  fragment; pairs with `plan` for round-trip honesty.

Each verb is a thin REST client over the operator's OAuth session.
No daemon-side coordinator. No new tables.

## Resource-name = resreg.Resource.Name (not table name)

The public manifest names map to **`resreg.Resource.Name`** — the
operator-facing contract per
[`../5/5-uniform-mcp-rest.md`](../5/5-uniform-mcp-rest.md#caller-and-resource-shape).
Backing tables are an implementation detail and may be renamed,
split, or merged without touching manifest files.

Per the same spec, the canonical operator-facing string for every
action is `<resource>.<action>` (e.g. `routes.create`,
`grants.update`). 7/5 uses that exact vocabulary — no second naming
layer, no aliases, no internal table names in the manifest surface.

## Manifest shape

A manifest is a YAML document with one top-level map keyed by
**resource name**. Each resource maps to a list of rows; each row
is the same JSON shape the resreg `create`/`update` REST adapter
already accepts.

```yaml
# atlas.yaml — flat resource namespace, no daemon section keys.
groups:
  - folder: atlas
    product: assistant
    model: claude-opus-4-5
    persona_ref: './atlas/PERSONA.md'
    memory_ref: './atlas/MEMORY.md'

acl:
  - principal: 'user:abc123'
    action: 'tasks:write'
    scope: 'atlas/'
    effect: 'allow'

routes:
  - match: 'telegram:user/atlas-bot'
    target_folder: 'atlas'
    seq: 100

scheduled_tasks:
  - target_jid: 'telegram:user/abc123'
    prompt: '/compact-memories episodes day'
    cron: '0 2 * * *'
    context_mode: 'isolated'

secrets:
  # metadata only — blob set via `arizuko secret set atlas/slack <value>`
  - scope: 'folder:atlas'
    name: 'slack'

proxyd_routes:
  - path: '/api/atlas'
    backend: 'http://atlasd:8080'
    auth: 'jwt'
    gated_by: 'atlas:read'

web_routes:
  - jid: 'web:atlas'
    owner_folder: 'atlas'

invites:
  - target_glob: 'atlas/'
    max_uses: 1
    expires_at: '2026-06-01T00:00:00Z'
```

There are **no daemon section keys** (`gated:` / `proxyd:` / …).
The apply tool consults the live registry to resolve each resource
name to its owning daemon at dispatch time. This keeps the
operator contract clean of deploy-unit topology: if a future
daemon split moves `proxyd_routes` ownership, manifests stay valid.
The resource-name itself is the contract.

Where two resources happen to share a name across daemons (e.g.
`routes` in `gated` and `proxyd_routes` in proxyd), the registry
already disambiguates by the `Resource.Name` field — different
names, no collision.

## Two-table-class model

One physical SQLite file (`messages.db`). On Postgres it would be
two schemas (`config.*`, `runtime.*`); on SQLite it is
**documentation discipline** — no naming prefixes, no separate
files, just a clear rule about which tables each class owns and
what may touch them.

**Config tables** — operator-authored intent, rebuilt from YAML on
every startup/reload. YAML is truth; the DB is a queryable index.

```
groups  acl  acl_membership  routes  route_tokens  web_routes
scheduled_tasks  network_rules  proxyd_routes
invites  onboarding_gates  secrets
```

**Runtime tables** — system-generated record, append-only, never
touched by apply/reload.

```
messages  chats  topics  turns  turn_results
audit_log  cost_log  cli_audit  ipc_audit  secret_use_log
auth_sessions  session_log  task_run_logs  identity_codes
system_messages  router_state  group_watchers
chat_reply_state  pane_sessions
```

**Rules that must be upheld:**

1. `apply`/reload only writes to config tables. It never touches
   runtime tables.
2. Runtime tables are never DELETE'd in bulk — only by explicit
   retention/purge commands.
3. Config tables have no migration history — the YAML is the
   migration. Runtime tables have full migration history in
   `store/migrations/`.
4. Cross-class JOINs are allowed and expected (dashd, reporting).
   The split is a write-discipline boundary, not a query boundary.
5. No new table goes into the config class without a corresponding
   entry in the resource catalog and apply support. A table that
   isn't manifest-addressable belongs in the runtime class.

**Reload atomicity:** `BEGIN; DELETE config tables; INSERT from
YAML; COMMIT`. SQLite WAL gives readers snapshot isolation during
the transaction — they see the old config until commit, then
instantly see the new config. No bloat: freed pages go to the
freelist and are reused by the next INSERT cycle.

**Operator-generated config** (onboarding groups, ad-hoc grants,
dynamically issued route tokens) lives in runtime tables directly
— it is not in YAML. `arizuko export` snapshots these rows into
YAML for an operator who wants to promote them into the static
manifest.

## Resource catalog (v1)

Built from `store/migrations/*.sql`. Each row is a candidate for
resreg + manifest. Hot-tier tables are deliberately excluded.

| Resource           | Owning daemon        | What it carries                                                                  |
| ------------------ | -------------------- | -------------------------------------------------------------------------------- |
| `groups`           | gated                | folder registration + product + model                                            |
| `acl`              | gated                | unified ACL rules ([`../4/9`](../4/9-acl-unified.md) )                           |
| `acl_membership`   | gated                | group membership for `group:` principals                                         |
| `routes`           | gated                | message routing table                                                            |
| `route_tokens`     | gated                | external caller → folder bindings ([`W-webhook-routes.md`](W-webhook-routes.md)) |
| `web_routes`       | gated                | web-channel route bindings (webd JIDs)                                           |
| `scheduled_tasks`  | gated (timed reader) | cron entries                                                                     |
| `secrets`          | gated                | metadata only — blob set out-of-band                                             |
| `network_rules`    | gated                | crackbox egress allowlist                                                        |
| `proxyd_routes`    | proxyd               | reverse-proxy route table                                                        |
| `invites`          | onbod                | invitation tokens                                                                |
| `onboarding_gates` | onbod                | per-instance onboarding policy                                                   |

Hot-tier tables (`messages`, `chats`, `audit_log`, `cost_log`,
`cli_audit`, `ipc_audit`, `task_run_logs`, `turn_results`,
`pane_sessions`, `secret_use_log`, `auth_sessions`,
`group_watchers`, `chat_reply_state`, `session_log`,
`identity_codes`, `system_messages`, `router_state`) are **not
manifest-addressable** — they are queue, cursor, audit, or
in-flight state, not intent.

## Markdown vs YAML

The rule is mechanical: **if it's a row, YAML. If it's a paragraph,
Markdown.**

- **YAML** carries table-shaped rows for cold-tier resources
  listed above. Manifest apply mutates daemon state through
  resreg.
- **Markdown** carries prose: `PERSONA.md`, `MEMORY.md`,
  `.diary/YYYYMMDD.md`, `decisions/<sha>.md`, `skills/<name>/SKILL.md`,
  `PRODUCT.md`. Markdown files are **referenced** from YAML by
  relative path; their bodies are not ingested into resreg rows.
- 7/3 already commits these files to git directly (per-turn
  commit for `MEMORY.md` + `.diary/`; per-folder write for
  `PERSONA.md` + `skills/`). 7/5 does not duplicate that path —
  YAML carries the reference; the file lives where 7/3 says it
  lives.
- Apply validates that referenced files **exist** and records
  their content hash in the row (`persona_sha256`,
  `memory_sha256`). The hash is the bridge — if the operator
  edits `PERSONA.md` without re-applying, drift detection
  surfaces the mismatch.

This keeps the orthogonality 7/3 already drew: prose is a git
file, not a row payload. Frontmatter on Markdown files carries
the file's own metadata (status, depends), never row data
back-doored from YAML.

## Apply lifecycle

1. **Parse.** YAML → typed Go structs. Strict mode: unknown
   resource keys reject; unknown row fields reject. Any error
   here aborts before touching the DB.
2. **Validate.** Each row is validated against the resource
   schema in the binary (not a live registry fetch — the apply
   tool and gated are co-versioned). Unknown fields reject.
   Referenced files (`persona_ref`, `memory_ref`) must exist;
   content hash recorded. Any error aborts before touching the DB.
3. **Plan.** Diff validated manifest rows against current config
   DB. Produce a human-readable delta: rows to add, rows to
   update, rows unchanged. Unchanged rows are noted but not
   re-inserted. `arizuko plan` stops here (non-mutating).
4. **Apply.** Open a single SQLite transaction on the config DB.
   Delete all manifest-owned rows. Insert all validated rows from
   the manifest. Commit. On any error: rollback; old DB unchanged.
5. **Report.** Print the plan delta + `ok` or the error that
   caused rollback.

`arizuko get <resource>` queries the live config DB and emits
the corresponding manifest YAML fragment.

**`get` round-trip rules.** `arizuko get <resource>` MUST emit
the exact YAML shape that `apply` accepts — no extra fields, no
omitted fields, no reordering of keys. Secret rows emit metadata
only (`scope`, `name`); the `value`/`ciphertext` field is never
present in `get` output regardless of caller scope. Markdown
references emit the relative path and recorded content hash;
the body is never inlined.

## Dependency ordering

Full rebuild inserts all rows in one transaction. Foreign-key
constraints are deferred until commit, so manifest row order
doesn't matter — all parent rows (groups) and child rows
(grants, routes) land together atomically. No class ordering
needed; the transaction handles it.

## Atomicity model

**Fully atomic via full rebuild.** Because the config DB is
transitory and YAML is truth, apply is not an upsert loop — it
is a `BEGIN; DROP + INSERT all rows; COMMIT`. The whole manifest
applies or nothing does. No per-row error accumulation, no
class-gated skipping, no partial state.

This is the key reason SQL is the right substrate for config:

- A document store has no cross-document transaction — you'd
  need 2PC or accept partial apply.
- A file-per-resource approach (plain YAML-as-files with no DB)
  has no atomic swap — a crash mid-write leaves torn state.
- SQLite gives `BEGIN/COMMIT` for free. Full rebuild in one
  transaction is cheaper to implement and reason about than any
  reconciliation loop.

On validation failure (unknown resource key, bad row shape,
missing referenced file), the transaction is never opened —
the error is returned before any mutation. On runtime DB
failure mid-insert, the transaction rolls back and the old
config DB continues serving.

Idempotency: applying the same manifest twice rebuilds the
same DB twice. Second run produces identical state. Safe to
re-run after any failure.

## Splitting + composition

- One manifest = one YAML file. `arizuko apply foo.yaml
bar.yaml…` reads all files, merges, plans, dispatches as one
  run.
- Files compose **additively per resource** — two files
  contributing `routes: [...]` are unioned.
- Duplicate primary keys across files (or within one file) is
  a **parse-time error**. No "last wins" merge.
- No `include:` directives. Flat composition only.

## Secret safety

Secret blobs **never** appear in:

1. Manifest YAML — only metadata (scope, name).
2. Markdown sidecars — prose files are content-hashed but never
   carry blob material.
3. `arizuko plan` diff output — secret rows show only metadata
   diff; the blob is "set" or "unset", never echoed.
4. Per-row error payloads — daemon errors on `secrets` rows
   strip any inadvertent value before logging.
5. Audit-log rows for `secrets.create` / `secrets.update` —
   per resreg's `params_summary` redaction rules.

Setting a blob is a separate command — `arizuko secret set
<scope>/<name> <value>` — that POSTs to a dedicated endpoint
gated to operator only. Manifests describe metadata; secrets
flow through their own channel. Trust boundary unchanged from
[`7/2 ## secrets`](2-data-model.md#secrets).

## Status is not in the manifest

Manifests are **intent**. Live state lives in the daemon's
SQLite cache and is reported by `arizuko get`. Manifests never
carry `status:` / `applied_at:` / `last_error:` blocks; same
boundary `kubectl` draws between spec and status.

## Cross-refs

- [`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) —
  resreg defines the per-resource handler + REST + MCP surface
  the apply tool talks to.
- [`../7/2-data-model.md`](../7/2-data-model.md) — the cold/warm/hot
  tier boundary; this spec touches cold tier only.
- [`../7/3-git-as-truth.md`](../7/3-git-as-truth.md) — `agents.toml`
  references in 7/3 are superseded by YAML manifests as
  specified here. The git tree carries `<product>.yaml` +
  Markdown sidecars; the gateway commits them as 7/3 describes.
- [`../7/4-data-ingestion-curation-eventing.md`](../7/4-data-ingestion-curation-eventing.md)
  — Q2 (product manifest extends to ingestion?) and Q5
  (cross-product eventing) remain open. 5/36 gives them a place
  to land (extend the resource catalog when those questions
  resolve), not a closed answer.
- [`32-tenant-self-service.md`](32-tenant-self-service.md)
  — Phase C secret layering composes with the `secrets` resource
  here.

## Non-goals

- No live reload / file watcher (v2 work).
- No DAG dependency resolution beyond the three-class
  ordering above.
- No drift remediation (`plan` shows drift; operator
  re-applies).
- No web UI for editing manifests.
- No multi-instance apply (one instance per CLI run).
- No transactional cross-daemon rollback.
- No format conversion from existing imperative `arizuko group
add` CLI verbs — those stay for ad-hoc operator work; manifests
  are the declarative path.
- No product composition / mixin semantics — that's open in 7/4
  Q2 and lives in a later spec.
- No eventing primitives — that's open in 7/4 Q5.

## Open questions

1. **Registry endpoint shape.** `GET /v1/_resources` returning
   the daemon's `resreg.Resource` catalog as JSON Schema vs a
   custom shape. JSON Schema is verbose but tool-friendly.
   Decide at implementation time.

2. **`--prune` ownership boundary.** Additive multi-file
   composition + `--prune` is a foot-gun: a rerun with a
   subset of files would delete rows the missing files own.
   v1 leaves `--prune` deliberately unimplemented; deletion is
   explicit per-row via `state: absent` (a row whose only
   meaning is "delete this PK if present"). Spec for a
   ownership-aware `--prune` (per-label / per-source-file)
   lands separately if real demand surfaces.

3. **Cross-class dependencies.** A `scheduled_task` referencing
   an `invite` that lands later requires two apply runs in v1.
   Is that acceptable forever, or do we need a DAG resolver in
   v2? Lean: wait for a real user collision before adding
   complexity.

4. **Resource catalog evolution.** As resreg coverage grows
   (per `7/1`'s migration matrix), new resources become
   manifest-addressable. The mechanism is uniform — add a
   `resreg.Resource` and it's automatically in
   `GET /v1/_resources`. No 7/5 amendment needed.

5. **dashd as a manifest editor.** dashd ships read-only views
   of cold-tier state today. Should it gain a "manifest
   editor" tab? Out of scope for 7/5; tracked as future dashd
   work.

## Acceptance

- `arizuko apply foo.yaml` round-trips through resreg: dry-run
  via `plan` shows diff; apply mutates state + writes audit
  rows; second apply is a no-op.
- Per-row error reporting; class-gated skipping on failure.
- `arizuko get <resource>` emits a YAML fragment that
  re-applies to a no-op against the same instance (round-trip
  honesty).
- Secret blobs never appear in YAML, plan output, or error
  messages.
- `make test -short` passes; integration tests cover the apply
  tool + at least one resource per active daemon.

## Pointers

- Plan: [`.ship/plan-7-5-yaml-manifests.md`](../../.ship/plan-7-5-yaml-manifests.md)
- Oracle critique: [`.ship/oracle-7-5-round1.md`](../../.ship/oracle-7-5-round1.md)
- resreg implementation: `resreg/resreg.go`,
  `resreg/README.md`
- First two resreg adopters: `proxyd/resource.go`,
  `webd/routes_mcp.go`
