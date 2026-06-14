---
status: shipped
shipped: 2026-06-14
depends: specs/5/5-uniform-mcp-rest.md, specs/8/2-data-model.md
---

# specs/5/36 — YAML manifests: transport dump/import for cold-tier config

> **DECISION.** The SQLite DB is authoritative. YAML manifests are a
> transport dump/import — `pg_dump` / `pg_restore` for the cold tier — not
> a continuously-synced source of truth. No DB→YAML sync, no startup-apply,
> no SIGHUP-reload. `specs/8/3-git-as-truth.md`'s continuously-synced
> cold-tier-config is superseded; committing an `export` dump to git is fine
> (8/3 itself is unedited — read its `agents.toml` references through this lens).

## Why

8/2's cold/warm/hot boundary leaves `agents.toml` unspecified. This spec
replaces it with a YAML manifest carrying an instance's cold-tier config:
ACL, routes, secrets metadata, scheduled tasks, proxyd routes, web routes,
network rules, group registration. Tokens (`invites`, `route_tokens`) are
imperative-only in v1.

`arizuko export` writes a point-in-time dump of cold-tier tables to YAML;
`arizuko apply file.yaml` restores the cold tier from a dump, rebuilding all
config tables in one SQLite transaction. YAML changes only on `export`;
runtime MCP/REST row ops change the DB, not the YAML. A dump never claims to
be live, so "drift" is a non-concept.

Product composition, cross-product subscriptions, and ingestion semantics
([`8/4`](../8/4-data-ingestion-curation-eventing.md) Q2 + Q5) remain open — this
spec gives them a place to land later, not an answer.

## Surface

- `arizuko export` — **dump** all cold-tier config tables to YAML
  (`manifest/`; file names are informational — any file may hold any
  resource kinds, see Manifest directory layout). Point-in-time; not synced.
- `arizuko apply <file>…` — **restore** the cold tier from a dump: read
  manifest dir or file(s), validate, rebuild config tables in one SQLite
  tx, report diff. **Rebuild scope:** per resource, DELETE+INSERT is scoped
  to the folders the manifest mentions (`DELETE … WHERE folder IN (<manifest
scope>)`). A row's omission deletes it only within a mentioned scope;
  groups/scopes absent from the manifest are untouched. Instance-global
  resources (no group wrapper) rebuild wholesale.
- `arizuko get <resource>[/<name>]` — dump live DB state as a YAML
  fragment for the named resource (a scoped `export`).
- `arizuko plan <file>…` — non-mutating; prints diff vs live state, no
  writes. The pre-apply preview — it shows exactly what a scoped restore
  would change (within the mentioned scopes) before you commit it.

## Manifest directory layout

`manifest/` is a flat directory of YAML files for one deployment.
**File names are informational only** — content, not name, determines what
config a file holds. Any file may contain any resource kinds.
`arizuko apply manifest/` reads all `*.yaml` files, merges resource lists by
PK, and applies the union in one transaction. Files compose additively;
duplicate primary keys across files are a parse-time error. The apply tool
never interprets file names; an operator may use one `everything.yaml` or
split arbitrarily.

**Document schema** — group folder is the top-level key; group config fields
and owned resources nest flat beneath it. Instance-global resources
(`proxyd_routes`, `onboarding_gates`, `network_rules`) appear as top-level
resource-kind keys with no group wrapper. Folder paths containing `/` must be
quoted.

```yaml
# atlas.yaml — one group plus a nested child
atlas:
  product: assistant # assistant | oracle | … (default assistant)
  model: claude-opus-4-7
  acl:
    - principal: user:sub_a1b2c3
      action: 'tasks:*'
      scope: atlas/
      effect: allow
  acl_membership:
    - child: user:sub_a1b2c3
      parent: group:editors
  routes:
    - seq: 0
      match: 'room=1234567890'
      target: atlas
  web_routes:
    - path_prefix: /pub/atlas/
      access: public
      folder: atlas
  scheduled_tasks:
    - id: atlas-compact
      owner: system
      chat_jid: atlas
      prompt: '/compact-memories episodes day'
      cron: '0 2 * * *'
      status: active
  secrets: # metadata only; blob set via `arizuko secret set` (§ Secret safety)
    - key: openai
  network_rules:
    - folder: atlas
      target: api.openai.com

'atlas/eng': # quoted: nested group key
  product: assistant

# base.yaml — instance-global resources (no group wrapper)
proxyd_routes:
  - path: /slack/
    backend: http://slakd:8080
    auth: public
    gated_by: SLACK_BOT_TOKEN
    preserve_headers: ['X-Slack-Signature', 'X-Slack-Request-Timestamp']
onboarding_gates:
  - gate: invite-only
    limit_per_day: 10
    enabled: true
network_rules:
  - folder: '' # empty folder = instance-global
    target: api.anthropic.com
```

## Three transports, one row schema

REST, MCP, and YAML are three transports over the **same row schema**,
defined once in Go by `resreg.Resource` and reused by all three. Drift is
structurally impossible — they share one handler.

|             | REST                            | MCP                           | YAML                              |
| ----------- | ------------------------------- | ----------------------------- | --------------------------------- |
| Verb        | HTTP method (POST/PATCH/DELETE) | Tool name (`acl.create`, `…`) | DROP + INSERT (rebuild per scope) |
| Identity    | URL path (`/groups/atlas/acl`)  | Tool args                     | YAML nesting (group key)          |
| Row fields  | request body                    | tool args                     | row map                           |
| Batching    | one row per call                | one row per call              | many rows, one tx                 |
| CAS version | — (single-row, serialized)      | — (single-row, serialized)    | `config_version:` manifest header |

Only **row fields** are part of `resreg.Resource`. Verb, identity, batching,
and version are transport envelopes — owned by the transport. The apply tool
parses YAML → unwraps the envelope (nesting, `config_version:`) → calls the
same handler REST POST and MCP `create` call.

### The row-schema half of `resreg.Resource`

`5/5`'s `Resource` carries the transport half (`{Name, Endpoints, MCPTools,
Authz, Handler, Store}`). This spec is authoritative for the **row-schema
half** the engine adds — `RowType`, `Table`, `PKFields`, `Scope`, `Hooks`,
`SkipApplyRebuild`:

```go
type Resource struct {
    // ... transport fields from 5/5 (Name, Endpoints, MCPTools, Authz, Handler, Store) ...
    RowType          reflect.Type  // canonical row Go struct, as a value: reflect.TypeOf(Row{})
    Table            string        // backing SQLite table
    PKFields         []string      // PK column(s), from pk:"true" struct tags
    Scope            ScopeFn       // per-resource scope metadata (which field, or none, holds the folder)
    Hooks            Hooks         // optional Validate/OnInsert callbacks
    SkipApplyRebuild bool          // true → apply never DELETE+INSERTs (e.g. secrets)
}
```

The YAML parser, REST handlers, and MCP tools all decode into instances of
`RowType`. Struct tags (`db:"…" yaml:"…" json:"…"`) define wire shape once;
adding a column edits the struct in one place and all three transports
follow. Without `RowType` the "one schema" claim is fictional and each
transport drifts silently; with it, drift is a compile error.

### Schema-driven CRUD

Adding a manifest-addressable resource is a small Go file: a struct, table
name, PK declaration, a `Register` call, and optional hooks. The engine
handles the **strict subset** — scalar columns, no NULLs, single-column or
declared-composite PK, RFC3339 timestamps. Resources outside that subset
declare hooks; the engine still does parse/emit/REST/MCP wiring. This
**replaces** today's ad-hoc per-resource readers (e.g. `store/groups.go`'s
six readers for one group row), not a greenfield addition.

**Tagged struct as the contract:**

```go
package webroutes

type Row struct {
    PathPrefix string `db:"path_prefix" yaml:"path_prefix" json:"path_prefix" pk:"true"`
    Access     string `db:"access"      yaml:"access"      json:"access"`
    Folder     string `db:"folder"      yaml:"folder"      json:"folder"`
}

func init() {
    resreg.Register(resreg.Resource{
        Name:    "web_routes",
        Table:   "web_routes",
        RowType: reflect.TypeOf(Row{}),   // value, not pointer
        Scope:   resreg.GroupScope("Folder"), // field that genuinely holds the folder
    })
}
```

`web_routes` is the clean scope example: its `Folder` field IS the folder,
so scoped DELETE derives directly. `routes` is **not** — `routes.target`
carries `#observe` / `#topic` fragments (it isn't column-equal to a folder,
see FK posture), so it needs per-resource scope metadata rather than a bare
field name.

From that struct, reflection over `db:`/`yaml:`/`json:` tags generates all
of these with **zero** per-resource code: `SELECT *` scan, `INSERT`, scoped
`DELETE`, YAML parse/emit, JSON in/out, the generic REST handler (reads
`Resource.Name` from URL), the MCP tool pair (`<name>.create`/`.delete`),
`arizuko export <name>`, and `arizuko plan` (diff manifest rows vs `SELECT *`
by PK).

The generic core is **one engine, written once** (~300–500 LOC):

```go
// resreg/engine.go
func (r *Resource) ScanAll(db *sql.DB) (any, error)            // reflect SELECT *
func (r *Resource) Insert(tx *sql.Tx, row any) error           // reflect INSERT
func (r *Resource) DeleteScope(tx *sql.Tx, scope string) error // reflect DELETE per Resource.Scope
func (r *Resource) ParseYAML(data []byte) (any, error)         // yaml.Unmarshal
func (r *Resource) EmitYAML(rows any) ([]byte, error)          // yaml.Marshal
```

`DeleteScope` cannot infer the folder from `RowType` alone: `acl` and
`acl_membership` have **no folder column** (their scope lives in
`acl.scope` glob / membership edges, not a plain folder field). The
per-resource `Resource.Scope` metadata supplies the rule — which field (if
any) holds the folder, or that the resource scopes by `scope`-glob /
rebuilds wholesale. The engine reads `Scope`, never guesses from the struct.

Apply ≈ 80 lines, Export ≈ 30 (`secrets` `SkipApplyRebuild` skips the
DELETE+INSERT):

```go
for _, r := range resreg.All() {
    if r.SkipApplyRebuild { continue }   // e.g. secrets — blob set imperatively
    rows, _ := r.ParseYAML(manifest[r.Name])
    r.DeleteScope(tx, scope)             // scope rule from r.Scope
    for _, row := range rows { r.Insert(tx, row) }
}
```

**Per-resource cost.** Simple resource (`routes`, `acl`, `acl_membership`,
`web_routes`, `proxyd_routes`, `onboarding_gates`, `network_rules`): ~30 LOC,
fully engine-driven. Complex: `groups` +~50 (JSON-blob `container_config`
hook, nullable `model`), `secrets` +~50 (encrypt/decrypt hook on
`value`), `scheduled_tasks` +~10 (RFC3339). Ceiling ~150 LOC.

**The engine handles shape, not semantics.** Per-resource hooks (optional
`Validate` / `OnInsert` callbacks) cover: validation beyond types ("folder
must exist", runs in-tx); derived fields (`created_at = now()`, RFC3339
parse, cost-cap clamps); encryption (secret blobs through enc/dec); cross-row
constraints (unique-within-scope, FK lookups); delete cleanup (group removal
must not cascade message history — purge is a separate verb). Hooks are the
**only** per-resource code; SELECT/INSERT/parse/emit stay in the engine.

## OpenAPI emission

The same reflection emits an OpenAPI 3.1 document per daemon — no `huma`,
`swag`, or codegen. Subsumes the former openapi-discoverable spec.

```go
// resreg/openapi.go
func OpenAPI(daemon, baseURL string, resources []string) ([]byte, error)
func OpenAPIHandler(daemon string, resources []string) http.HandlerFunc
```

For each `Resource` with `RowType != nil`:

- `components.schemas.<Name>` reflected from struct fields (`json:` tag →
  property name; Go kind → JSON Schema type; `omitempty` → not `required`).
- `paths./v1/<name>` GET (list) + POST (create); `paths./v1/<name>/{pk}`
  PATCH + DELETE. Composite PKs collapse to one URL parameter (description
  flags URL-encode separators).
- Standard errors (400/401/403/404/409/500) defined once in
  `components.responses`, `$ref`-d from every operation.
- One `servers[]` from `baseURL`. Title `arizuko <daemon> API`, version `v1`.

`OpenAPIHandler` caches the JSON for the process lifetime. **Endpoint is
public** (schemas describe surface, not data) — mount BEFORE auth middleware.

Per-daemon ownership (post-split owners per [`E-routd.md`](E-routd.md),
[`P-runed.md`](P-runed.md), [`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)
§ "Daemon ownership of `/v1/*`"):

| Daemon | Owned resources                                                                                                                                    |
| ------ | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| routd  | groups, routes, web_routes, acl, acl_membership, secrets, network_rules (residual config + conversation tables; inherits gated's schema authority) |
| timed  | scheduled_tasks                                                                                                                                    |
| onbod  | onboarding_gates                                                                                                                                   |
| authd  | — (signing keys / JWKs / sessions; no manifest-addressable config rows)                                                                            |
| proxyd | proxyd_routes (operator-composed enforcement point; see [`35-proxyd-standalone.md`](35-proxyd-standalone.md))                                      |
| runed  | — (execution runtime: spawns / session_log / mcp_tokens; all runtime tables, not config)                                                           |
| webd   | — (reads `web_routes` from routd; doc is informational)                                                                                            |
| dashd  | — (HTMX operator UI; CRUD lives in the owning daemons above)                                                                                       |

Daemons with no owned resources still emit `/openapi.json` so the aggregator
page (`/pub/arizuko/reference/openapi.html`) lists every daemon uniformly.

## Optimistic locking (`config_version`)

`config_meta` is a single-row config-class table holding a monotonic
integer `config_version`. **One bump per apply transaction**, not per row.

**Every writer ADVANCES `config_version`; only YAML apply CHECKS it
(CAS).** MCP/REST single-row mutations bump the counter in their tx so a
later apply detects them — but they don't carry a version to compare
against, because they are single-row, write-and-read in one call, serialized
by `BEGIN IMMEDIATE`; there is no stale snapshot to defend. YAML apply IS the
stale-snapshot pattern (export at T, edit for minutes/hours, apply at T+N
after MCP/REST may have committed), so it is the one writer that CAS-checks
the stamped version before writing and rejects on mismatch.

**Export** stamps the current DB version into the manifest header
(`config_version: 42`). **Apply** checks it before writing:

- DB version == manifest `config_version` → proceed; advance to N+1 on commit.
- mismatch → reject: "config changed since export; re-export or use --force."

A manifest with no `config_version` is rejected in strict mode. `--force`
skips the check and writes unconditionally (last-writer-wins; still advances
the counter). Without CAS, two operators both exporting at v42 and both
applying would silently clobber each other; the version forces a re-export
that surfaces the conflict.

### CAS implementation

**(1) Version table + bootstrap migration.**

```sql
-- Singleton: id pinned to 1 by CHECK so there is exactly one row to CAS on.
CREATE TABLE config_meta (
  id      INTEGER PRIMARY KEY CHECK(id = 1),
  version INTEGER NOT NULL DEFAULT 0
);
-- Bootstrap: count existing config rows so v1 is non-zero on instances
-- with pre-existing state. First export then sees a real version;
-- without back-fill, every fresh-install operator would need --force
-- on first apply against a populated DB. Upsert keeps the single row.
INSERT INTO config_meta (id, version)
  SELECT 1,
    (SELECT COUNT(*) FROM groups)            +
    (SELECT COUNT(*) FROM acl)               +
    (SELECT COUNT(*) FROM acl_membership)    +
    (SELECT COUNT(*) FROM routes)            +
    (SELECT COUNT(*) FROM web_routes)        +
    (SELECT COUNT(*) FROM scheduled_tasks)   +
    (SELECT COUNT(*) FROM network_rules)    +
    (SELECT COUNT(*) FROM proxyd_routes)     +
    (SELECT COUNT(*) FROM onboarding_gates)
  ON CONFLICT(id) DO UPDATE SET version = excluded.version;
```

`secrets` is **excluded** from the version-tracked set: routine blob
rotation (`arizuko secret set …`) must not invalidate every pending apply.
Secret metadata (`(scope_kind, scope_id, key)`) rebuilds from YAML like other
config; the encrypted blob is set imperatively and doesn't touch
`config_version`.

**(2) Bump once per writer tx, at the COMMIT site (not AFTER triggers).**

```
BEGIN IMMEDIATE;
  SELECT version FROM config_meta WHERE id = 1;
  -- mismatch (and not --force) → ROLLBACK; reject
  DELETE FROM <config tables> WHERE folder IN (<manifest scope>);   -- secrets skipped (SkipApplyRebuild)
  INSERT INTO <config tables> (...);
  UPDATE config_meta SET version = version + 1 WHERE id = 1;   -- single bump
COMMIT;
```

MCP/REST single-row mutations bump the same way inside their tx. Per-row
triggers are rejected: a bulk apply of N rows would advance by N, breaking
equality CAS. The invariant is monotonicity, not consecutive integers —
operators only compare equality.

**(3) One audit row per apply, not N** — summarizes actor, manifest digest,
rows added/updated/deleted per resource, final `config_version`.

**(4) `BEGIN IMMEDIATE`** acquires the RESERVED lock at tx start so
concurrent applies / MCP mutations / apply-vs-MCP all serialize; WAL readers
unaffected. Without it, two concurrent applies could both pass CAS at v42 and
the second's DELETE+INSERT would wipe the first's writes.

## New-group filesystem prep (post-restore)

`apply` is a restore: after the config tx commits, each group row may
reference an on-disk dir that doesn't yet exist. For every group folder in
the DB without a complete on-disk dir, `apply` calls
`container.SetupGroup(folder)` post-commit. A failed `SetupGroup` (disk full,
perm error) surfaces as an apply error, not swallowed — a row without its dir
makes inbound routing `docker run` against a missing path and exit 125.

`arizuko repair` re-runs the filesystem-prep step in isolation against the
live DB (idempotent), safe at any time.

## Group directory lifecycle

Group filesystem state (skills, `.claude/`, prototype) is **eventually
consistent with the DB**, not transactional — filesystem ops can't join a
SQLite tx.

- `apply` writes group rows in the tx; SetupGroup runs after COMMIT.
- On partial SetupGroup failure, the row exists but the directory is
  incomplete; `arizuko repair` re-runs SetupGroup for every such row.
- Removing a group from the manifest deletes its row on next apply (see Group
  removal semantics) but **does not delete the directory**. `arizuko group
purge <folder>` does full removal.

## Group removal semantics

**DECISION: when apply removes a `groups` row, active routing state is cleared
in the same tx; runtime history is not.**

**Active routing side-channels cleared inside the DELETE tx** (live refs that
would silently misroute if left dangling):

- `chats.sticky_group` → NULL where it points to the removed folder.
- `chat_reply_state.engaged_folder` → cleared.
- `group_watchers` → DELETE rows where `observer` OR `source` is the folder.
- `router_state` → clear any cached pointers for the folder.

These are runtime tables, but removing a group implies the operator wants it
gone from active routing even if history persists.

**Runtime history left intact** (keeps the orphaned `folder` string,
inaccessible via group views but queryable for forensics):
`messages.{chat_jid,folder}`, `audit_log.folder`, `cost_log.folder`,
`secret_use_log.folder`, `task_run_logs` (cascades from scheduled_tasks).

To fully erase a group (history included), `arizuko group purge <folder>`
(config DELETE + history DELETE + rmdir) — intentionally imperative,
destructive in a way YAML apply is not.

**`plan` output warns on group removal:**

```
$ arizuko plan
groups:
  - atlas    (REMOVE: clears 3 sticky_group + 1 engaged_folder + 2 watchers;
              strands 1247 message rows, 89 audit rows.
              `arizuko group purge atlas` to delete history.)
```

## Manifest shape

**Resource names map to `resreg.Resource.Name`**, the operator-facing
contract per
[`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md#caller-and-resource-shape)
(`<resource>.<action>` vocabulary, no aliases, no internal table names).
Backing tables may be renamed/split/merged without touching manifest files.

Group folder paths are top-level keys; owned resources nest flat beneath.
There are **no daemon section keys** (`gated:` / `proxyd:`) — the apply tool
resolves each resource name to its owning daemon at dispatch, so a future
daemon split leaves manifests valid.

**Secrets in YAML carry metadata only** (`(scope_kind, scope_id, key)`);
blobs set via `arizuko secret set`. Folder-scoped (default) infers folder
from group nesting and declares only `key:`; user-scoped adds an explicit
`user:` field. Parser maps these to `(folder, atlas, openai)` and `(user,
sub_a1b2c3, github_token)` — same implicit-from-nesting rule as `acl` /
`scheduled_tasks`.

```yaml
atlas:
  secrets:
    - key: openai # → (folder, atlas, openai)
    - user: sub_a1b2c3 # → (user, sub_a1b2c3, github_token)
      key: github_token
```

**Group fields** — all optional except the folder key itself:

| Field                     | Type   | Notes                                                               |
| ------------------------- | ------ | ------------------------------------------------------------------- |
| `product`                 | string | Type tag: `assistant`, `oracle`, … Default: `assistant`             |
| `model`                   | string | Override model for this group. Inherits instance default if omitted |
| `open`                    | bool   | Public group (visible to all users). Default: `true`                |
| `observe_window_messages` | int    | Max messages in observe-mode context window                         |
| `observe_window_chars`    | int    | Max chars in observe-mode context window                            |
| `cost_cap_cents_per_day`  | int    | Spend cap. Default: 0 (unlimited)                                   |

`container_config` is not manifest-addressable — it is written by the
container runner and treated as infra state.

Changing `product` only updates the DB column — it does **not** re-seed
group directory files (the prototype copy happens once at creation via
`container.SetupGroup`). New groups via YAML: apply inserts the row in the tx,
`SetupGroup` runs post-commit (see Group directory lifecycle); direct `mkdir`
is forbidden per CLAUDE.md.

## Two-table-class model

One physical SQLite file (`messages.db`), two table classes by
**documentation discipline** — no prefixes, no separate files, just a rule
about which tables each class owns.

**Config tables** — operator-authored cold-tier config. `apply` rebuilds
these from a YAML dump only on explicit restore (never at startup/reload).

```
groups  acl  acl_membership  routes  web_routes
scheduled_tasks  network_rules  proxyd_routes  onboarding_gates  secrets
```

`invites` and `route_tokens` are not manifest-addressable in v1 —
CLI/MCP only. See "Tokens are not in v1 manifests."

**Runtime tables** — system-generated record, append-only, never
touched by `apply`.

```
messages  chats  topics  turns  turn_results
audit_log  cost_log  cli_audit  ipc_audit  secret_use_log
auth_sessions  session_log  task_run_logs  identity_codes
system_messages  router_state  group_watchers
chat_reply_state  pane_sessions
```

**Rules that must be upheld:**

1. `apply` only writes to config tables. **The one named exception:** when
   apply removes a `groups` row, it clears that group's routing
   side-channel state in the same tx — `chats.sticky_group`,
   `chat_reply_state.engaged_folder`, `group_watchers`, `router_state` —
   so a removed group can't silently misroute (see Group removal
   semantics). It writes no other runtime tables.
2. Runtime tables are never DELETE'd in bulk — only by explicit
   retention/purge commands.
3. Both classes have full migration history in `store/migrations/`;
   the DB schema is authoritative for both. (The YAML dump carries
   config-table _rows_, not schema — restoring an old dump into a newer
   schema is the operator's concern, same as `pg_restore`.)
4. Cross-class JOINs are allowed and expected (dashd, reporting).
   The split is a write-discipline boundary, not a query boundary.
5. No new table goes into the config class without a corresponding
   entry in the resource catalog and apply support. A table that
   isn't manifest-addressable belongs in the runtime class.
6. **No daemon may cache config-table rows in memory** (normative). One
   shared SQLite file, one host; an indexed read is cheaper than any cache
   invalidation, and in-memory config caches create stale-read windows that
   make apply semantics undefined. Implementing this spec includes an audit
   pass removing every existing cache. Known offenders:
   `proxyd/resource.go:routesResource` (`proxyd_routes` under `sync.RWMutex`,
   logged in `bugs.md`); `gateway/*.go` `s.AllRoutes()` callers holding
   results across requests; `dashd/*.go` cached lookups. The one allowed
   cache is `sync.Map[backendURL]*httputil.ReverseProxy` for connection reuse
   — it caches connections, not config rows; the row that picked the URL is
   re-read per request.

**Restore atomicity:** `BEGIN; DELETE scoped config rows; INSERT from YAML;
COMMIT` (per-folder DELETE scoped to the folders the manifest mentions;
instance-global resources rebuild wholesale; `secrets` skips DELETE+INSERT).
All daemons see new config on their next DB read — no signals, no reload
endpoints, no cache invalidation. WAL gives readers snapshot isolation
during the tx; freed pages return to the freelist.

**Operator-generated config** (onboarding groups, ad-hoc grants, dynamically
issued route tokens) lives directly in config tables; `arizuko export`
snapshots it into YAML to promote into the static manifest.

## Resource catalog (v1)

Built from `store/migrations/*.sql`; hot-tier tables excluded.

Owning daemon is the post-split owner ([`E-routd.md`](E-routd.md),
[`P-runed.md`](P-runed.md)); the legacy column names the pre-split source
table in `gated`'s monolithic `messages.db` (the cutover copies it into the
new owner's DB, drops the source).

| Resource           | Apply mode  | PK (natural key)                                        | Owning daemon | Legacy source table (pre-split) |
| ------------------ | ----------- | ------------------------------------------------------- | ------------- | ------------------------------- |
| `groups`           | rebuild     | `folder`                                                | routd         | gated                           |
| `acl`              | rebuild     | `(principal, action, scope, params, predicate, effect)` | routd         | gated                           |
| `acl_membership`   | rebuild     | `(child, parent)`                                       | routd         | gated                           |
| `routes`           | rebuild     | `(seq, match, target)`                                  | routd         | gated                           |
| `web_routes`       | rebuild     | `path_prefix`                                           | routd         | gated                           |
| `scheduled_tasks`  | rebuild     | `id`                                                    | timed         | gated                           |
| `secrets`          | export-only | `(scope_kind, scope_id, key)`                           | routd         | gated                           |
| `network_rules`    | rebuild     | `(folder, target)`                                      | routd         | gated                           |
| `proxyd_routes`    | rebuild     | `path`                                                  | proxyd        | gated                           |
| `onboarding_gates` | rebuild     | `gate`                                                  | onbod         | gated                           |
| `invites`          | —           | n/a — CLI/MCP only (see Tokens are not in v1 manifests) | onbod         | gated                           |
| `route_tokens`     | —           | n/a — CLI/MCP only                                      | routd         | gated                           |

**`secrets` apply-mode is export-only / no-rebuild.** The engine sets
`SkipApplyRebuild: true` on the `secrets` `Resource`: apply never
DELETE+INSERTs secret rows, because the encrypted blob is set imperatively
(`arizuko secret set`) and a rebuild would wipe it. `export` and `get`
still emit secret metadata (never the blob); apply validates and reports
diff but skips the DELETE+INSERT for this resource. (`SkipApplyRebuild` is
also why `secrets` is excluded from the `config_version` set below.)

**PK declaration is load-bearing.** Each resource's struct tags its PK fields
(`pk:"true"` single-column; multiple tagged for composite). The engine uses
the PK to deduplicate across files and to scope DELETE during rebuild.
Resources marked `—` are imperative-only (system-generated `token` /
`token_hash` PKs, not round-tripped — see Tokens are not in v1 manifests).

Hot-tier tables (`messages`, `chats`, `audit_log`, `cost_log`, `cli_audit`,
`ipc_audit`, `task_run_logs`, `turn_results`, `pane_sessions`,
`secret_use_log`, `auth_sessions`, `group_watchers`, `chat_reply_state`,
`session_log`, `identity_codes`, `system_messages`, `router_state`) are **not
manifest-addressable** — queue, cursor, audit, or in-flight state, not intent.

## Markdown vs YAML

**If it's a row, YAML. If it's a paragraph, Markdown.** YAML carries
table-shaped cold-tier rows (operator intent: ACL, routes, tasks); apply
writes them to DB in one tx. Markdown carries prose (`PERSONA.md`,
`MEMORY.md`, `.diary/`, `decisions/<sha>.md`, `skills/<name>/SKILL.md`,
`PRODUCT.md`) — agent context living in the group directory, never manifest
rows, never referenced from YAML, never content-hashed in the DB; 8/3 manages
their git lifecycle.

## Apply lifecycle

1. **Parse.** YAML → typed Go structs. Strict: unknown resource keys or row
   fields reject. Aborts before touching the DB.
2. **Validate.** Each row validated against the resource schema in the binary
   (apply tool + the owning daemon co-versioned). Aborts before touching the
   DB.
3. **Plan.** Diff validated rows vs current config DB → human-readable delta
   (add/update/unchanged). `arizuko plan` stops here (non-mutating).
4. **Apply.** One SQLite tx, **scoped DELETE+INSERT**: per resource,
   DELETE the rows within the folders the manifest mentions
   (`DELETE … WHERE folder IN (<manifest scope>)`) then INSERT the validated
   rows; absent scopes are untouched. Instance-global resources (no group
   wrapper) rebuild wholesale. `secrets` (`SkipApplyRebuild`) is validated
   but never DELETE+INSERTed. COMMIT. On any error: rollback; old DB
   unchanged.
5. **Report.** Print the plan delta + `ok`, or the rollback error.

**`arizuko get <resource>` round-trip.** Queries the live config DB and emits
a manifest YAML fragment that re-applies to a no-op — exact shape `apply`
accepts, no extra/omitted fields, no reordering. Secret rows emit metadata
only (`scope_kind`, `scope_id`, `key`); the `value` blob never appears.

**Canonical key order is mandatory** (Go map iteration is
non-deterministic): top-level `config_version` first, then group folders
(lexicographic), then global resource keys (lexicographic); within a group,
resource keys in catalog order; within a list, rows sorted by PK (composite
PKs by concatenated string). Two consecutive `export`s must be byte-identical
on an unchanged DB or file hashing / git diffs break.

## FK posture

**FKs are ON globally.** `store/store.go` sets `PRAGMA foreign_keys=ON`
per connection. Declared FKs are enforced.

**Three FKs are declared in v1:**

| FK                                             | Migration | ON DELETE | Rationale                                                                                                                                         |
| ---------------------------------------------- | --------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `task_run_logs(task_id) → scheduled_tasks(id)` | 0011      | CASCADE   | runtime → config; run history evaporates with the task definition.                                                                                |
| `web_routes(folder) → groups(folder)`          | 0068      | CASCADE   | config → config; URL pinning to a removed group must die with the group.                                                                          |
| `route_tokens(owner_folder) → groups(folder)`  | 0069      | CASCADE   | runtime → config; webhook tokens minted by a removed group become unroutable; CASCADE deletes them silently (correct — the URL would 404 anyway). |

**Posture rule:** declare a FK when (a) the reference is row-shaped
(single target table, not polymorphic) and (b) on parent delete, the
runtime expects either silent CASCADE of the children or explicit
RESTRICT — never silent dangling. Every other cross-table reference
in the schema is intentionally string-typed:

| Reference                                                                                    | Why string, not FK                                                                                                                                                                                                  |
| -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `acl.principal`, `acl.scope`, `acl_membership.{child,parent}`                                | Polymorphic encodings (`user:sub_xyz`, `group:eng`, `**`, `service:authd`). No single target table. The string IS the canonical form.                                                                               |
| `secrets.scope_id`                                                                           | Polymorphic by `scope_kind` column (`folder` \| `user`).                                                                                                                                                            |
| `network_rules.folder`                                                                       | Empty-folder rows (`folder=''`) carry instance-global rules (migration 0037 seed). A FK to `groups.folder` would reject these legitimate rows. SQLite has no `FOREIGN KEY ... WHERE` predicate to exclude them.     |
| `routes.target`                                                                              | Carries a `#observe` fragment in some rows (migration 0054). Not column-equal to any folder.                                                                                                                        |
| `scheduled_tasks.chat_jid`                                                                   | Polymorphic: folder OR typed JID (`web:`, `hook:`, `telegram:`, …).                                                                                                                                                 |
| `messages.{chat_jid,folder}`, `audit_log.folder`, `cost_log.folder`, `secret_use_log.folder` | Runtime history. Group removal MUST strand these rows for forensics; CASCADE deletes them silently (wrong), RESTRICT blocks legitimate removals (worse). `arizuko group purge` is the separate verb.                |
| `chats.sticky_group`, `chat_reply_state.engaged_folder`, `group_watchers.{observer,source}`  | Active routing state. Spec mandates explicit clearing in the apply tx (see Group removal semantics) so the cleanup is auditable and ordered — a silent SET NULL / CASCADE would bypass the engine's audit emission. |
| `proxyd_routes.path`, `onboarding_gates.gate`                                                | No cross-table reference to declare.                                                                                                                                                                                |

## Dependency ordering

**Insert in resource-catalog order**, which places `groups` first so
`web_routes` / `route_tokens` FKs resolve. DELETE reverses (children first);
the `ON DELETE CASCADE` FKs make `DELETE FROM groups WHERE folder = ?` remove
children automatically (explicit per-table DELETEs also safe, idempotent). If
a future config-to-config FK introduces a cycle, set `PRAGMA
defer_foreign_keys=ON` at tx start (checks defer to COMMIT). No cycle in v1.

## Atomicity model

**Fully atomic via scoped rebuild.** Apply is not an upsert loop — it is
`BEGIN; DELETE scoped config rows; INSERT all rows; COMMIT` (per-folder
DELETE scoped to the folders the manifest mentions; instance-global
resources rebuild wholesale; `secrets` skips DELETE+INSERT). The whole
manifest applies or nothing does; no per-row error accumulation, no partial
state. On validation failure the tx is never opened (error returned before
any mutation); on DB failure mid-insert it rolls back and the old config DB
keeps serving. Idempotent: re-applying the same manifest produces identical
state, safe to re-run after any failure.

## Tokens are not in v1 manifests

**DECISION: resources with a system-generated secret as PK
(`invites.token`, `route_tokens.token_hash`) are excluded from v1
manifests.** A full rebuild would wipe live tokens; preserving them needs
either secret values in YAML or a "name" indirection layer — both
disproportionate. For v1, tokens are managed imperatively:

- `arizuko invite issue <target_glob> [--max-uses N] [--expires <ts>]`,
  `arizuko invite list`, `arizuko invite revoke <token>`.
- Same surface for `route_tokens` via `arizuko token …`.
- MCP tools: `invites.create`, `invites.revoke`, etc.

These mutations still bump `config_version` and audit-log (so operators can
detect drift), but tokens never appear in `manifest/`.

**Future work — v2 encrypted token export** (not build-required): operator
supplies an encryption key; export emits `token: 'enc:AES-GCM:<b64>'`; apply
decrypts with the same key (tokens are PKs, so INSERT-or-UPDATE upsert).
Key is operator-local; ciphertext is git-safe. ~150 LOC when demand surfaces.

## Splitting + composition

`apply foo.yaml bar.yaml…` (or `apply manifest/`, every `*.yaml`) reads all
files, merges, plans, and applies as one run. Files compose **additively per
resource** — two files contributing `routes:` produce a union of rows. PK
collision rules (PK declared per Resource catalog):

- Same PK, identical payload across files → silently deduplicated.
- Same PK, differing payload across files → parse-time error with location:
  `"acl_membership PK (user:alice, group:engineers) conflicts in
atlas.yaml:42 and shared.yaml:17"`.
- Same PK twice in one file → parse-time error (always a bug).

No `include:` directives; flat composition only. File reads are deterministic
(lexicographic) so errors are reproducible, but the merged set is
order-independent (composition is associative).

## Secret safety

Secret blobs **never** appear in: (1) manifest YAML (metadata only); (2)
Markdown sidecars; (3) `arizuko plan` output (blob shown as "set"/"unset");
(4) per-row error payloads (stripped before logging); (5) audit-log rows for
`secrets.create`/`.update` (resreg `params_summary` redaction). Setting a
blob is a separate operator-gated command, `arizuko secret set <scope>/<name>
<value>`. Trust boundary unchanged from
[`8/2 ## secrets`](../8/2-data-model.md#secrets).

## Status is not in the manifest

A dump carries cold-tier config rows only; live state is read by `arizuko
get`. Dumps never carry `status:` / `applied_at:` / `last_error:` — the same
spec/status boundary `kubectl` draws.

## Cross-refs

- [`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) — resreg defines the
  per-resource handler + REST + MCP surface the apply tool talks to.
- [`../8/2-data-model.md`](../8/2-data-model.md) — cold/warm/hot tier
  boundary; this spec touches cold tier only.
- [`../8/3-git-as-truth.md`](../8/3-git-as-truth.md) — **reframed, not
  adopted** (see lead DECISION). Its `agents.toml` placeholder is replaced;
  its continuously-synced premise is rejected. 8/3 is unedited.
- [`../8/4-data-ingestion-curation-eventing.md`](../8/4-data-ingestion-curation-eventing.md)
  — Q2/Q5 open; extend the resource catalog when they resolve.
- [`32-tenant-self-service.md`](32-tenant-self-service.md) — Phase C secret
  layering composes with the `secrets` resource here.

## Non-goals

- No live reload / file watcher; no DB→YAML sync (re-`export` to refresh).
- No DAG dependency resolution beyond the catalog ordering above.
- No web UI for editing manifests; no multi-instance apply; no transactional
  cross-daemon rollback.
- No conversion from imperative `arizuko group add` verbs (they stay for
  ad-hoc work; manifests are the declarative path).
- No product composition / mixin semantics or eventing primitives (open in
  8/4 Q2/Q5, later spec).

## Open questions

1. **Registry endpoint shape.** `GET /v1/_resources` as JSON Schema vs a
   custom shape — decide at implementation time.
2. **Cross-class dependencies.** A `scheduled_task` referencing an `invite`
   landing later needs two apply runs in v1. DAG resolver in v2 if a real
   collision surfaces.
3. **dashd as a manifest editor** — out of scope, future dashd work.

(Resolved: `--prune` / `state: absent` are cut — within a mentioned scope, a
row's absence prunes it (scoped DELETE+INSERT); scopes absent from the
manifest are untouched. Resource-catalog evolution is uniform — adding a
`resreg.Resource` makes it manifest-addressable automatically.)

## Acceptance

**Implementation must be a unified framework using standard Go idioms
— `reflect`, struct tags, `database/sql`, `gopkg.in/yaml.v3`,
`encoding/json`. No DSLs, no codegen, no third-party ORMs.**

Functional:

- `arizuko apply foo.yaml` round-trips through resreg: dry-run via
  `plan` shows diff; apply mutates state + writes one audit row;
  second apply is a no-op.
- `arizuko export` produces byte-identical output across two
  consecutive runs if the DB is unchanged (canonical ordering).
- `arizuko get <resource>` emits a YAML fragment that re-applies
  to a no-op (round-trip honesty).
- Secret blobs never appear in YAML, `plan` output, or error
  messages.
- All 10 declarative resources go through the engine — no
  bypass code paths remain after the migration.
- Every HTTP-serving daemon (routd, runed, authd, timed, onbod, proxyd,
  webd, dashd) exposes `GET /openapi.json` returning a valid OpenAPI 3.1
  doc for its owned resources. The document is engine-generated, public,
  cached for the process lifetime. Subsumes spec `5/4-openapi-discoverable.md`.

Testability:

- **Engine unit tests** in `resreg/engine_test.go` cover scan/insert/
  delete/parse/emit against an in-memory SQLite, **without any
  arizuko-specific resource**. The engine is testable in isolation
  using a synthetic `TestResource` struct.
- **Per-resource unit tests** in each `<pkg>/resource_test.go` cover
  the resource's struct + hooks against the same in-memory pattern.
  Each resource testable independently.
- **E2E tests** in `cmd/arizuko/apply_test.go` exercise the full
  apply lifecycle (parse → CAS → DELETE+INSERT → COMMIT → SetupGroup)
  against a real tempfile SQLite + tempdir manifest, for every
  resource.
- `make test -short` covers engine + per-resource. `make test`
  covers the e2e suite. `make smoke` includes a post-deploy
  `arizuko export | apply -` round-trip against a live instance.

No-cache audit:

- `proxyd/resource.go:routesResource` mutex+snapshot pattern removed;
  proxyd queries DB per request.
- `gateway/*.go` `s.AllRoutes()` callers audited; any cached results
  held across requests are eliminated.
- Acceptance test asserts `grep -r "sync.RWMutex" proxyd/` returns
  nothing related to config caching.

## Pointers

- Plan: [`.ship/plan-7-5-yaml-manifests.md`](../../.ship/plan-7-5-yaml-manifests.md)
- Oracle critique: [`.ship/oracle-7-5-round1.md`](../../.ship/oracle-7-5-round1.md)
- resreg implementation: `resreg/resreg.go`,
  `resreg/README.md`
- First two resreg adopters: `proxyd/resource.go`,
  `webd/routes_mcp.go`
