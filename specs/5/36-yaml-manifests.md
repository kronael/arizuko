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
instance: ACL, routes, secrets metadata, scheduled tasks,
proxyd routes, web routes, network rules, group registration.
Tokens (`invites`, `route_tokens`) are imperative-only in v1; see
"Tokens are not in v1 manifests."

The mechanism is direct: one CLI verb (`arizuko apply`) parses YAML,
validates each row, and rebuilds all config tables in one SQLite
transaction. YAML is the source of truth; the DB is the queryable
index. No REST dispatch, no daemon-side state machine — one tx,
one file.

## What this spec is

The **carrier format** for cold-tier configuration and the **apply**
mechanics (one SQLite tx, mutation sync, optimistic locking).
Product composition, cross-product subscriptions, and ingestion
semantics ([`7/4`](4-data-ingestion-curation-eventing.md) Q2 + Q5)
remain open — 5/36 gives them a place to land later, not an answer.

## Surface

- `arizuko apply <file>…` — read manifest dir or file(s), validate,
  rebuild config tables in one SQLite tx, report diff.
- `arizuko plan <file>…` — same but non-mutating; prints diff vs
  live state, no writes.
- `arizuko get <resource>[/<name>]` — dump live DB state as a YAML
  fragment for the named resource.
- `arizuko export` — dump all config tables as a manifest dir
  (`manifest/`), one file per resource kind.

## Manifest directory layout

`manifest/` is a flat directory of YAML files for one deployment.
**File names are informational only** — the content determines what
config is in a file, not the name. Any file may contain any resource
kinds. `arizuko apply manifest/` reads all `*.yaml` files, merges
resource lists by key, and applies the union in one transaction.
Files compose additively; duplicate primary keys across files are a
parse-time error.

```
manifest/
  base.yaml     ← instance-global config (proxyd_routes, onboarding_gates, ...)
  atlas.yaml    ← config for the atlas group
  krons.yaml    ← config for the krons group
  shared.yaml   ← cross-group ACL or routes (optional, any name)
```

These names are conventions, not constraints. An operator can use a
single `everything.yaml` or split arbitrarily. The apply tool does
not interpret file names.

**Document schema** — same keys in every file, merged at apply time.
Group folder is the top-level key; group config fields and owned
resources nest flat beneath it.

```yaml
# atlas.yaml — config for the atlas group

atlas:
  product: assistant
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

  secrets:
    - key: openai
    - key: anthropic

  network_rules:
    - folder: atlas
      target: api.openai.com
```

```yaml
# krons.yaml — krons group plus nested children

krons:
  product: assistant
  model: claude-opus-4-7

  acl:
    - principal: '**'
      action: '*'
      scope: krons/
      effect: allow

'krons/eng':
  product: assistant

  acl:
    - principal: group:engineers
      action: 'chat:*'
      scope: krons/eng/
      effect: allow

  acl_membership:
    - child: user:sub_alice
      parent: group:engineers
    - child: user:sub_bob
      parent: group:engineers

'krons/eng/sre/oncall':
  product: assistant
  model: claude-haiku-4-5

  scheduled_tasks:
    - id: oncall-digest
      owner: system
      chat_jid: krons/eng/sre/oncall
      prompt: '/digest last 24h'
      cron: '0 8 * * *'
      status: active
```

```yaml
# base.yaml — instance-wide config (proxyd routes, gates, global rules)

proxyd_routes:
  - path: /tele/
    backend: http://teled:8080
    auth: public
    gated_by: TELEGRAM_TOKEN
  - path: /whap/
    backend: http://whapd:8080
    auth: public
    gated_by: WHATSAPP_TOKEN
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
  - folder: ''
    target: anthropic.com
  - folder: ''
    target: api.anthropic.com
```

In-flight files use a dot-prefix — hidden, never at rest:

```
manifest/.atlas.yaml   ← only during a mutation, deleted or renamed immediately
```

`.gitignore` entry: `manifest/.*`. Startup sweep: delete any `manifest/.*`
orphans — they are crash evidence (see Mutation sync).

## Mutation sync

Every resreg config mutation (MCP or CLI) rewrites the owning group's
manifest file synchronously, before returning to the caller.

**Home-file rule** — the file a row belongs to is determined by its
group folder field (no DB tracking column needed):

| Resource                                                      | Home-file key                                                  | Home file                |
| ------------------------------------------------------------- | -------------------------------------------------------------- | ------------------------ |
| group config fields                                           | the namespace key itself                                       | `manifest/<folder>.yaml` |
| `acl`                                                         | folder extracted from `scope`                                  | `manifest/<folder>.yaml` |
| `acl_membership`                                              | containing group key in document                               | `manifest/<folder>.yaml` |
| `routes`                                                      | `target`                                                       | `manifest/<folder>.yaml` |
| `web_routes`                                                  | `folder`                                                       | `manifest/<folder>.yaml` |
| `scheduled_tasks`                                             | `chat_jid` (is the folder)                                     | `manifest/<folder>.yaml` |
| `secrets`                                                     | containing group key (folder-scoped) or explicit `user:` field | `manifest/<folder>.yaml` |
| `network_rules` (group-scoped)                                | `folder`                                                       | `manifest/<folder>.yaml` |
| `proxyd_routes`, `onboarding_gates`, `network_rules` (global) | — (base)                                                       | `manifest/base.yaml`     |

Protocol per mutation:

```
1. determine home file: manifest/atlas.yaml
2. read current home file → merge in the new/updated/deleted row
3. serialize merged state → write to manifest/.atlas.yaml
4. BEGIN tx
5.   write row to DB
6. COMMIT
7.   rename(manifest/.atlas.yaml, manifest/atlas.yaml)  ← atomic
   on COMMIT failure: ROLLBACK, delete manifest/.atlas.yaml
   on rename failure: manifest/.atlas.yaml is the orphan recovery (see below)
```

Rename is **post-commit**. If the process dies between COMMIT and
rename, `manifest/.atlas.yaml` survives as an orphan. On next
startup: orphan detected → rename it → YAML caught up. DB is briefly
ahead but always recoverable. Pre-commit crash: ROLLBACK, delete
orphan, `atlas.yaml` unchanged.

No background goroutines. Sync is on the handler's critical path.

**Same-filesystem assumption.** `rename(2)` is atomic only within
one filesystem on Linux. The spec assumes `manifest/` and any
in-flight `manifest/.*.yaml` live on the same mount. Bind-mounting
`manifest/` from another filesystem breaks atomicity guarantees.
Operators who do this are on their own.

## Three transports, one row schema

REST, MCP, and YAML are three transports over the **same row schema**.
The schema is defined once in Go by `resreg.Resource` and reused by all
three. Drift between transports is structurally impossible — they share
one handler.

|             | REST                            | MCP                           | YAML                              |
| ----------- | ------------------------------- | ----------------------------- | --------------------------------- |
| Verb        | HTTP method (POST/PATCH/DELETE) | Tool name (`acl.create`, `…`) | DROP + INSERT (rebuild per scope) |
| Identity    | URL path (`/groups/atlas/acl`)  | Tool args                     | YAML nesting (group key)          |
| Row fields  | request body                    | tool args                     | row map                           |
| Batching    | one row per call                | one row per call              | many rows, one tx                 |
| CAS version | — (single-row, serialized)      | — (single-row, serialized)    | `config_version:` manifest header |

Only **row fields** are part of `resreg.Resource`. Verb, identity, batching,
and version are transport envelopes — owned by the transport, not the
resource.

The apply tool parses YAML → unwraps the envelope (nesting,
`config_version:`) → calls the same handler that REST POST and MCP
`create` call. One handler, three callers.

### `RowType` on `resreg.Resource`

`resreg.Resource` today is `{Name, Endpoints, MCPTools, Authz, Handler,
Store}` — no field declares the row column set. v1 adds:

```go
type Resource struct {
    // ... existing fields ...
    RowType reflect.Type  // pointer to the canonical row Go struct
}
```

The YAML parser, REST handlers, and MCP tools all decode into instances
of `RowType`. Struct field tags (`yaml:"…" json:"…"`) define wire shape
once per resource. Adding a column means editing the struct in one
place; all three transports follow.

Without `RowType` the "one schema" claim is fictional: each transport
would carry its own struct and drift silently. With it, drift is a
compile error.

### Schema-driven CRUD: minimize the per-resource code

Goal: adding a new manifest-addressable resource is a small Go file:
a struct, table name, primary key declaration, a `Register` call, and
optional hooks. The engine handles the **strict subset** of resources
— scalar columns, no NULLs, single-column or declared-composite PK,
RFC3339 timestamps. Resources outside that subset declare hooks; the
engine still handles parse/emit/REST/MCP wiring.

The codebase today has ad-hoc per-resource readers (`store/groups.go`
has six different readers for one group row across `model`, `open`,
`observe_window_*`, `cost_cap_*` — see issue tracker). The reflective
engine **replaces** these scattered readers with one — this is a
migration of existing code, not a greenfield addition. Drift between
the engine's struct and the per-call-site readers is a known risk
that the refactor closes.

**Tagged struct as the contract:**

```go
package routes

type Row struct {
    Seq    int    `db:"seq"    yaml:"seq"    json:"seq"`
    Match  string `db:"match"  yaml:"match"  json:"match"`
    Target string `db:"target" yaml:"target" json:"target"`
}

func init() {
    resreg.Register(resreg.Resource{
        Name:    "routes",
        Table:   "routes",
        RowType: reflect.TypeOf(Row{}),
        Scope:   resreg.GroupScope("Target"), // field that holds the folder
    })
}
```

That ~15-line file gives us, mechanically:

| Surface                 | Mechanism                                                                                                   | LOC added per resource |
| ----------------------- | ----------------------------------------------------------------------------------------------------------- | ---------------------- |
| `SELECT *`              | reflect over `db:` tags → build column list, scan into struct                                               | 0                      |
| `INSERT`                | reflect over `db:` tags → build placeholder list, bind values                                               | 0                      |
| `DELETE WHERE` scope    | reflect over `Scope` field name → `DELETE WHERE <col> = ?`                                                  | 0                      |
| YAML parse              | `yaml.Unmarshal(bytes, reflect.New(RowType))`                                                               | 0                      |
| YAML emit               | `yaml.Marshal(rows)` over `[]Row`                                                                           | 0                      |
| JSON in/out             | same, with `json:` tags                                                                                     | 0                      |
| REST handler            | one generic handler reads `Resource.Name` from URL, decodes JSON into RowType, calls insert/delete          | 0                      |
| MCP tool                | one generic tool generator produces `<name>.create` / `.delete` from `Resource.Name`, args = RowType fields | 0                      |
| `arizuko export <name>` | `SELECT * FROM <Table>` → emit YAML                                                                         | 0                      |
| `arizuko plan`          | diff parsed manifest rows vs `SELECT *` results by PK                                                       | 0                      |

The generic core is **one engine, written once**:

```go
// resreg/engine.go — sketch, ~200 LOC total

func (r *Resource) ScanAll(db *sql.DB) (any, error) { /* reflect SELECT * */ }
func (r *Resource) Insert(tx *sql.Tx, row any) error { /* reflect INSERT */ }
func (r *Resource) DeleteScope(tx *sql.Tx, scope string) error { /* reflect DELETE */ }
func (r *Resource) ParseYAML(data []byte) (any, error)  { /* yaml.Unmarshal */ }
func (r *Resource) EmitYAML(rows any) ([]byte, error)   { /* yaml.Marshal */ }
```

Apply tool becomes ~80 lines total:

```go
for _, r := range resreg.All() {
    rows, _ := r.ParseYAML(manifest[r.Name])
    r.DeleteScope(tx, scope)
    for _, row := range rows { r.Insert(tx, row) }
}
```

Export tool becomes ~30 lines: iterate resources, scan, marshal.

**Per-resource cost ceiling:** ~80 LOC for the struct, the
`Register` call, and any non-trivial validation hook. Resources
that need custom logic (FK checks, derived fields, encryption) add
hooks via optional `resreg.Resource.Validate` / `OnInsert` callbacks.

### How this compares to other systems

| System              | Schema source       | Generates                              | Mechanism          |
| ------------------- | ------------------- | -------------------------------------- | ------------------ |
| GORM (Go)           | struct tags         | SQL CRUD                               | reflection         |
| sqlc (Go)           | hand-written `.sql` | typed Go funcs                         | codegen            |
| Ent (Facebook)      | schema DSL in Go    | SQL + GraphQL + REST                   | codegen            |
| Hasura / PostgREST  | live DB schema      | REST + GraphQL                         | live introspection |
| Django models       | Python class        | migrations + ORM + admin UI            | metaclasses        |
| kubectl + CRDs      | OpenAPI schema      | apply/get/delete CLI + REST validation | live registry      |
| Terraform providers | provider schema DSL | HCL parser + state + plan/apply        | codegen + registry |
| etcd                | flat KV, protobufs  | Put/Range/Watch (no per-resource code) | uniform protocol   |

arizuko's resreg lands between **Hasura** (live introspection from
DB schema) and **kubectl + CRD** (registry of declarative types).
The trade-off:

- **Live DB introspection** (Hasura-style): zero per-resource code,
  but every column rename or DEFAULT change leaks through the API.
  Schema migrations become breaking changes for clients.
- **Static struct + reflection** (what 5/36 picks): one source file
  per resource, struct fields freeze the API contract independent
  of column names. Migration `ALTER TABLE foo RENAME COLUMN x TO y`
  is invisible to clients as long as the struct tag stays `db:"y"`.

The static-struct approach matches Go's idioms (reflect, struct
tags), needs no codegen step, and gives compile-time errors when
fields drift. Reflection cost is one-time at process start
(`reflect.TypeOf` is cached); steady-state queries use prepared
statements with the column list precomputed.

### What does NOT come for free

The schema-driven engine handles **shape**, not **semantics**.
Per-resource Go is still needed when:

- **Validation beyond types.** "Folder must exist" is not a struct
  tag; the resource's `Validate(row, tx)` callback runs in-tx.
- **Derived fields.** `created_at` set to `now()`; `expires_at`
  parsed from RFC3339; cost caps clamped to 0 ≤ N ≤ 1e9.
- **Encryption.** Secret blobs flow through `enc/dec` hooks, not
  raw struct fields.
- **Cross-row constraints.** Unique within scope, FK lookups.
- **Cleanup on delete.** Removing a group should not implicitly
  cascade message history — purge is a separate verb.

These live as small, optional hook methods on the resource type.
The point is that the hooks are the **only** per-resource code;
the bulk (SELECT/INSERT/parse/emit) stays in the engine.

### Engine sketch — how export/import comes "for free"

The engine relies on three Go primitives:

1. **`reflect`** — read `db:` tags to build SQL column lists; iterate
   struct fields to bind Scan/Exec arguments.
2. **`gopkg.in/yaml.v3` + `encoding/json`** — already use struct tags.
3. **`sql.Rows.Scan(scanTargets...)`** — accepts `[]any` of pointers,
   which reflection produces from the struct fields.

```go
// Export ALL resources to one YAML stream — ~10 lines.
func Export(db *sql.DB, out io.Writer) error {
    manifest := map[string]any{
        "config_version": readVersion(db),
    }
    for _, r := range registry.All() {
        rows, err := r.ScanAll(db)
        if err != nil { return err }
        manifest[r.Name] = rows
    }
    return yaml.NewEncoder(out).Encode(manifest)
}

// Generic SELECT via reflection over `db:` tags.
func (r *Resource) ScanAll(db *sql.DB) (any, error) {
    cols := r.columnList()                 // cached at registration
    sql := "SELECT " + strings.Join(cols, ",") + " FROM " + r.Table
    rows, _ := db.Query(sql)
    defer rows.Close()
    slice := reflect.MakeSlice(reflect.SliceOf(r.RowType), 0, 16)
    for rows.Next() {
        row := reflect.New(r.RowType).Elem()
        targets := r.scanTargets(row)      // field addrs via reflect
        rows.Scan(targets...)
        slice = reflect.Append(slice, row)
    }
    return slice.Interface(), nil
}

// Apply: same machinery, reversed. ~15 LOC of new code.
func Apply(db *sql.DB, manifest map[string]any) error {
    tx, _ := db.BeginImmediate()
    defer tx.Rollback()
    if !casCheck(tx, manifest["config_version"]) {
        return ErrVersionMismatch
    }
    for _, r := range registry.All() {
        rows := r.parseRows(manifest[r.Name])   // YAML → []Row
        r.DeleteScope(tx, scope)
        r.InsertAll(tx, rows)                    // INSERT generated
    }
    tx.Exec("UPDATE config_meta SET version = version + 1")
    return tx.Commit()
}
```

**What's actually free per resource:** export/import, REST GET/POST
generic handlers, MCP tool registration (generated from `Resource.Name`
plus the struct fields), YAML round-trip, JSON in/out. Net per-resource
LOC for a simple resource (e.g., `routes`): ~30 LOC for the struct
declaration + register call.

**Honest accounting for complex resources:** `groups` adds ~50 LOC for
the JSON-blob `container_config` hook and nullable `model` field;
`secrets` adds ~50 LOC for the encrypt/decrypt hook on `enc_value`;
`scheduled_tasks` adds ~10 LOC for RFC3339 time format. Total
per-resource ceiling: ~150 LOC. Engine core: ~300-500 LOC.

The "free" claim is true for the strict-subset resources (`routes`,
`acl`, `acl_membership`, `web_routes`, `proxyd_routes`,
`onboarding_gates`, `network_rules`) and ~70% true for the rest.

## Optimistic locking (`config_version`)

`config_meta` is a single-row config-class table holding a monotonically
increasing integer: `config_version`. **One bump per apply transaction**,
not per row.

**CAS applies only to YAML apply.** MCP and REST mutations do not carry
the version. They are single-row, write-and-read in one call, serialized
by `BEGIN IMMEDIATE`. There is no stale snapshot for an MCP agent to
defend; an `If-Match` style header would force useless refetch loops
that don't prevent any real bug.

YAML apply IS the stale-snapshot pattern: export at T, edit for minutes
or hours, apply at T+N. Between T and T+N, MCP/REST may have committed
changes. Apply needs CAS to surface that drift.

**Export** stamps the current DB version into the manifest header:

```yaml
config_version: 42
```

**Apply** checks the DB version before writing:

- DB version == manifest `config_version` → proceed; advance to N+1 on
  commit.
- DB version != manifest `config_version` → reject: "config changed
  since export; re-export or use --force to override."

A manifest with no `config_version` field is rejected in strict mode.
`--force` skips the check and writes unconditionally (last-writer-wins;
still advances the counter).

**Why CAS:** without it, two operators can both export at v42, both
edit, both apply with stale-but-superseded manifests, and the second
silently overwrites the first. The version forces a re-export step that
surfaces the conflict.

The pattern is identical to etcd's `revision`, S3's `If-Match` ETag, and
Kubernetes's `metadata.resourceVersion` — except simpler because we only
need it on the bulk-apply path.

### CAS implementation

**(1) Version table + bootstrap migration.**

```sql
CREATE TABLE config_meta (version INTEGER NOT NULL DEFAULT 0);
-- Bootstrap: count existing config rows so v1 is non-zero on instances
-- with pre-existing state. First export then sees a real version;
-- without back-fill, every fresh-install operator would need --force
-- on first apply against a populated DB.
INSERT INTO config_meta (version)
  SELECT
    (SELECT COUNT(*) FROM groups)            +
    (SELECT COUNT(*) FROM acl)               +
    (SELECT COUNT(*) FROM acl_membership)    +
    (SELECT COUNT(*) FROM routes)            +
    (SELECT COUNT(*) FROM web_routes)        +
    (SELECT COUNT(*) FROM scheduled_tasks)   +
    (SELECT COUNT(*) FROM network_rules)    +
    (SELECT COUNT(*) FROM proxyd_routes)     +
    (SELECT COUNT(*) FROM onboarding_gates);
```

`secrets` is **excluded** from the version-tracked set. Routine
out-of-band blob rotation (`arizuko secret set …`) must not invalidate
every operator's pending manifest apply. Secret metadata (the
`(scope_kind, scope_id, key)` triple) is rebuilt from YAML on apply
along with the other config tables; the encrypted blob is set
imperatively and doesn't touch `config_version`.

**(2) Version bumps once per writer tx, not per row.** The bump is at
the writer's COMMIT site, not in AFTER triggers. Apply does:

```
BEGIN IMMEDIATE;
  SELECT version FROM config_meta;
  -- compare to manifest config_version
  -- if mismatch (and not --force): ROLLBACK; reject
  -- (re-read manifest files INSIDE the tx — see Mutation sync race)
  DELETE FROM <config tables> WHERE folder IN (<manifest scope>);
  INSERT INTO <config tables> (...);
  UPDATE config_meta SET version = version + 1;   -- single bump
COMMIT;
```

MCP/REST single-row mutations bump similarly inside their tx:
`BEGIN IMMEDIATE; ... write row(s) ...; UPDATE config_meta SET version
= version + 1; COMMIT;`.

Per-row triggers were considered and rejected: bulk apply of N rows
would advance the version by N, breaking equality CAS (manifest at 42,
DB at 642 after one apply, but the operator who exported at 642 cannot
distinguish "1 apply ago" from "642 mutations ago"). One bump per
writer tx keeps the counter human-meaningful.

**(3) Apply emits ONE audit row per apply, not N.** The audit-log row
summarizes: actor, manifest digest, rows added/updated/deleted per
resource, final config_version. Per-row audit would multiply log volume
by row count for every operator edit.

**(4) `BEGIN IMMEDIATE` for race-free serialization.** Acquires the
RESERVED lock at tx start so concurrent applies, concurrent MCP
mutations, and apply-vs-MCP all serialize cleanly. WAL readers stay
unaffected.

Without `BEGIN IMMEDIATE`, two concurrent applies could both see
version 42, both pass the CAS check, both commit, and the second's
DELETE+INSERT would wipe rows the first just wrote (visibility under
WAL is snapshot-isolated but writes serialize). `IMMEDIATE` forces
the second tx to wait for the first to commit, then it sees the
bumped version and rejects.

Per-row counting on bulk apply means `config_version` advances by N
(number of rows) per apply, not by 1. This is fine — the invariant
is monotonicity, not consecutive integers. Operators never read the
counter for arithmetic; they only compare equality.

## Startup sequence

Order is load-bearing. `gated` does these in sequence before opening
its listen socket:

```
1. Orphan recovery — scan manifest/ matching the exact pattern
   `\.[A-Za-z0-9_/-]+\.yaml$` (literal dot prefix + name + .yaml).
   Vim swapfiles (`*.swp`), editor backups (`*~`), and tempfiles
   from `sed -i` or other writers do NOT match and are ignored.
   For each orphan:
     - canonical `<name>.yaml` missing → promote: rename(orphan, canonical).
     - canonical exists AND mtime(orphan) > mtime(canonical) AND
       content(orphan) != content(canonical) → AMBIGUOUS. Bail with
       fatal: operator must reconcile manually (compare files, delete
       one, restart). Do NOT auto-promote — orphan might be from a
       crashed MCP mutation that the operator already integrated by
       hand into the canonical file.
     - canonical exists AND content(orphan) == content(canonical) →
       safe duplicate, delete orphan.
     - canonical mtime ≥ orphan mtime → orphan superseded, delete it.

2. Apply manifest/ — run `arizuko apply manifest/`. Lifecycle:
     a. BEGIN IMMEDIATE.
     b. SELECT config_version; compare to manifest header.
     c. Re-read manifest files INSIDE the tx (after BEGIN IMMEDIATE
        holds the RESERVED lock — see "Mutation sync race" below).
     d. DELETE config tables in scope; INSERT validated rows.
     e. UPDATE config_meta SET version = version + 1.
     f. COMMIT.

3. New-group filesystem prep — for each group folder in DB without
   a complete on-disk dir, call container.SetupGroup(folder).
   **This step is fatal-on-failure.** If a group row exists in the
   DB but SetupGroup cannot create the directory (disk full, perm
   error), gated exits with a fatal error and does NOT open the
   listen socket. The operator runs `arizuko repair` (idempotent
   re-run of step 3) then restarts.

4. Open listen socket.
```

**Why orphan recovery first.** Mutation sync does rename POST-commit
(see Mutation sync). If the process died between COMMIT and rename,
the DB is one mutation ahead of `<name>.yaml`. Promoting the orphan
catches the YAML up before apply reads it. Apply-first would commit
the stale YAML state, undoing the just-committed mutation when the
orphan is later promoted.

**Why filesystem prep fatal-on-failure.** A group row in the DB
without an on-disk dir means inbound messages routing to that group
would `docker run` against a missing path and exit 125. The cost
of bailing at startup is high (operator has to intervene); the cost
of opening the socket with broken groups is higher (silent message
drop). The bail is the right tradeoff.

`arizuko repair` is the operator escape hatch: it re-runs step 3 in
isolation against the live DB. Safe to invoke at any time.

On `SIGHUP`, gated re-runs steps 1–3 (no socket re-bind). SIGHUP
during an in-flight apply is **queued via `BEGIN IMMEDIATE`** — the
second tx blocks on the first's COMMIT, then sees the bumped version
and proceeds. SIGHUP does not interrupt anything mid-tx.

### Mutation sync race (parse-inside-tx)

The naive sequence "parse files outside the tx, then BEGIN IMMEDIATE"
has a race: between parse (t=0) and BEGIN (t=1), an MCP mutation can
rewrite a manifest file and commit its DB write. Apply now holds a
stale in-memory snapshot AND passes the CAS equality check (both
mutation and apply saw the same version pre-write). Apply's
DELETE+INSERT then silently overwrites the MCP mutation's row.

Fix: **parse twice**. First parse outside the tx (for validation and
version extraction); second parse inside the tx AFTER acquiring
RESERVED lock, comparing against `config_version`. If files changed
between the two parses (different SHA256), reject apply with "manifest
files changed during apply; retry."

This costs one extra parse but kills the race deterministically. No
OS-level file locks needed.

## Group directory lifecycle

Group filesystem state (skills, `.claude/`, prototype) is **eventually
consistent with the DB**, not transactional. The DB is authoritative.

- `apply` writes group rows in the tx; SetupGroup runs after COMMIT.
- If SetupGroup partially fails (disk full, permission error), the row
  exists in the DB but the directory is incomplete. `arizuko repair`
  re-runs SetupGroup for every group row without a complete directory.
- Removing a group from the manifest deletes its row on next apply
  (see Group removal semantics) but **does not delete the directory**.
  Operators run `arizuko group purge <folder>` for full removal.

This split is deliberate. Filesystem operations can't join a SQLite tx;
trying to make them atomic with COMMIT either requires 2PC (overkill)
or risks leaking orphan directories on row-insert failure. Letting the
filesystem be the slower mirror keeps apply simple and recoverable.

## Group removal semantics

When apply removes a `groups` row, **active routing state is
cleared in the same tx**; runtime history is not.

**Active routing side-channels cleared automatically:**

These tables hold _live_ references that would silently misroute if
left dangling. Apply clears them inside the `groups` DELETE tx:

- `chats.sticky_group` — set to NULL where it points to the removed
  folder. Otherwise sticky-routing keeps targeting a dead group.
- `chat_reply_state.engaged_folder` — cleared. Otherwise reply
  routing fires into the void.
- `group_watchers` — DELETE rows where `observer` OR `source` is the
  removed folder. Observe-mode UIs no longer surface stranded refs.
- `router_state` — clear any cached pointers (if present) for the
  folder.

These are runtime tables (not config), but the rule applies: removing
a group from the manifest implies the operator wants the group _gone
from active routing_, even if history persists.

**Runtime history is left intact:**

- `messages.chat_jid` / `messages.folder`
- `audit_log.folder`
- `cost_log.folder`
- `secret_use_log.folder`
- `task_run_logs` (cascades from scheduled_tasks)

These keep the orphaned `folder` string. They become inaccessible via
group views but stay queryable for forensics or migration.

The split is principled: **active state = cleared on removal; history
= preserved**.

Arizuko's broader posture is FK-light. The only declared FK between
any pair of tables is `task_run_logs → scheduled_tasks` (ON DELETE
CASCADE). All other cross-table references are unenforced strings.
Removing a group does not corrupt the schema; it only requires the
active-state cleanup above.

To fully erase a group (history included):

```
arizuko group purge <folder>   # config DELETE + history DELETE + rmdir
```

`purge` is intentionally imperative — it is destructive in a way YAML
apply is not.

**`plan` output warns on group removal:**

```
$ arizuko plan
groups:
  - atlas    (REMOVE: clears 3 sticky_group + 1 engaged_folder + 2 watchers;
              strands 1247 message rows, 89 audit rows.
              `arizuko group purge atlas` to delete history.)
```

Operators see the consequence before applying.

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

A manifest is a YAML document where **group folder paths are
top-level keys**. Group config fields and owned resources nest
flat beneath the group key. Instance-global resources
(`proxyd_routes`, `onboarding_gates`, `network_rules`) appear as
top-level resource-kind keys with no group wrapper.

There are **no daemon section keys** (`gated:` / `proxyd:` / …).
The apply tool resolves each resource name to its owning daemon at
dispatch time. If a future daemon split moves `proxyd_routes`
ownership, manifests stay valid — the resource name is the contract.

Folder paths with `/` must be quoted in YAML:

```yaml
# quoted path key for nested group
'corp/eng/sre/oncall':
  product: assistant
  model: claude-opus-4-7
  acl:
    - principal: group:sre
      action: 'chat:*'
      scope: corp/eng/sre/oncall/
      effect: allow
```

Secrets metadata only — blobs set via `arizuko secret set
folder:<folder>/<key> <value>`, never in YAML. DB schema is
`(scope_kind, scope_id, key)`. In YAML:

- Folder-scoped (default) — folder is inferred from group nesting;
  only `key:` is declared.
  ```yaml
  atlas:
    secrets:
      - key: openai
      - key: anthropic
  ```
- User-scoped — explicit `user:` field for the sub.
  ```yaml
  atlas:
    secrets:
      - user: sub_a1b2c3
        key: github_token
  ```

The parser maps these to DB rows: `(folder, atlas, openai)` and
`(user, sub_a1b2c3, github_token)`. No new serialization format is
invented — the implicit-from-nesting rule mirrors how `acl` and
`scheduled_tasks` already work under a group key.

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

**`product`** does **not** trigger re-seeding of group directory files
on apply — the prototype copy (skills, PERSONA.md, `.claude/`) happens
once at group creation via `container.SetupGroup`. Changing `product`
in the manifest only updates the DB column.

**New groups via YAML.** Apply inserts the group row in the tx;
`container.SetupGroup` runs post-commit to seed the directory. See
"Group directory lifecycle" for the full ordering and failure
semantics. `mkdir` directly is forbidden (per CLAUDE.md).

## Two-table-class model

One physical SQLite file (`messages.db`). On Postgres it would be
two schemas (`config.*`, `runtime.*`); on SQLite it is
**documentation discipline** — no naming prefixes, no separate
files, just a clear rule about which tables each class owns and
what may touch them.

**Config tables** — operator-authored intent, rebuilt from YAML on
every startup/reload. YAML is truth; the DB is a queryable index.

```
groups  acl  acl_membership  routes  web_routes
scheduled_tasks  network_rules  proxyd_routes  onboarding_gates  secrets
```

`invites` and `route_tokens` are not manifest-addressable in v1 —
CLI/MCP only. See "Tokens are not in v1 manifests."

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
6. **No daemon may cache config-table rows in memory.** All daemons
   share one SQLite file on one host; an indexed DB read costs
   microseconds and is cheaper than any cache invalidation scheme.
   In-memory config caches are bugs — they create stale-read windows
   that make apply semantics undefined.

   **This rule is normative, not aspirational.** Implementing 5/36
   includes an audit pass that removes every existing in-memory
   config cache. Known offenders to fix:
   - `proxyd/resource.go:routesResource` — caches `proxyd_routes`
     under `sync.RWMutex` (logged in `bugs.md`).
   - `gateway/*.go` — `s.AllRoutes()` callers that hold results
     across requests must be re-checked.
   - `dashd/*.go` — any cached config lookups for UI rendering.

   Resource-handle objects MAY hold one cache: `sync.Map[backendURL]
*httputil.ReverseProxy` for proxy connection reuse. This caches
   _connections_, not config rows; the row that picked the URL is
   re-read per request.

   Acceptance criterion: no successful 5/36 implementation can leave
   `routesResource`-style caches in place. The audit is part of the
   ship checklist.

**Reload atomicity:** `BEGIN; DELETE config tables; INSERT from
YAML; COMMIT`. All daemons see new config on their next DB read —
no signals, no reload endpoints, no cache invalidation. SQLite WAL
gives readers snapshot isolation during the transaction. No bloat:
freed pages go to the freelist and are reused by the next INSERT cycle.

**Operator-generated config** (onboarding groups, ad-hoc grants,
dynamically issued route tokens) lives in runtime tables directly
— it is not in YAML. `arizuko export` snapshots these rows into
YAML for an operator who wants to promote them into the static
manifest.

## Resource catalog (v1)

Built from `store/migrations/*.sql`. Each row is a candidate for
resreg + manifest. Hot-tier tables are deliberately excluded.

| Resource           | Apply mode | PK (natural key)                                        | Owning daemon        |
| ------------------ | ---------- | ------------------------------------------------------- | -------------------- |
| `groups`           | rebuild    | `folder`                                                | gated                |
| `acl`              | rebuild    | `(principal, action, scope, params, predicate, effect)` | gated                |
| `acl_membership`   | rebuild    | `(child, parent)`                                       | gated                |
| `routes`           | rebuild    | `(seq, match, target)`                                  | gated                |
| `web_routes`       | rebuild    | `path_prefix`                                           | gated                |
| `scheduled_tasks`  | rebuild    | `id`                                                    | gated (timed reader) |
| `secrets`          | rebuild    | `(scope_kind, scope_id, key)`                           | gated                |
| `network_rules`    | rebuild    | `(folder, target)`                                      | gated                |
| `proxyd_routes`    | rebuild    | `path`                                                  | proxyd               |
| `onboarding_gates` | rebuild    | `gate`                                                  | onbod                |
| `invites`          | —          | n/a — CLI/MCP only (see Tokens are not in v1 manifests) | onbod                |
| `route_tokens`     | —          | n/a — CLI/MCP only                                      | gated                |

**PK declaration is load-bearing.** Each resource's Go struct tags
the PK fields (`pk:"true"` for single-column; multiple fields tagged
for composite). The reflective engine uses the PK to deduplicate
across files (see Splitting + composition) and to scope DELETE
during rebuild.

Resources marked `—` are imperative-only in v1. They have system-
generated PKs (`token`, `token_hash`) that v1 declines to round-trip
through YAML (see Tokens are not in v1 manifests).

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

- **YAML** carries table-shaped rows for cold-tier resources listed
  above. Manifest apply writes them to DB in one transaction.
- **Markdown** carries prose: `PERSONA.md`, `MEMORY.md`,
  `.diary/YYYYMMDD.md`, `decisions/<sha>.md`, `skills/<name>/SKILL.md`,
  `PRODUCT.md`. These files live in the group directory; they are
  **not** manifest rows, not referenced from YAML, not content-hashed
  in the DB. 7/3 manages their git lifecycle independently.

Orthogonality: YAML carries operator intent (who can do what,
which routes exist, what tasks run). Markdown carries agent context
(persona, memory, diary). Neither borrows from the other's domain.

## Apply lifecycle

1. **Parse.** YAML → typed Go structs. Strict mode: unknown
   resource keys reject; unknown row fields reject. Any error
   here aborts before touching the DB.
2. **Validate.** Each row is validated against the resource schema
   in the binary (apply tool and gated are co-versioned). Unknown
   fields reject. Any error aborts before touching the DB.
3. **Plan.** Diff validated manifest rows against current config
   DB. Produce a human-readable delta: rows to add, rows to
   update, rows unchanged. Unchanged rows are noted but not
   re-inserted. `arizuko plan` stops here (non-mutating).
4. **Apply.** Open a single SQLite transaction on the config DB.
   Delete all manifest-owned rows. Insert all validated rows from
   the manifest. Commit. On any error: rollback; old DB unchanged.
5. **Report.** Print the plan delta + `ok` or the error that
   caused rollback.

`arizuko get <resource>` queries the live config DB and emits the
corresponding manifest YAML fragment.

**`get` round-trip rules.** `arizuko get <resource>` MUST emit the
exact YAML shape that `apply` accepts — no extra fields, no omitted
fields, no reordering of keys. Secret rows emit metadata only
(`scope_kind`, `scope_id`, `key`); the `enc_value` blob is never
present in `get` output regardless of caller scope.

**Canonical key order is mandatory.** Go's `map[string]…` iteration
is non-deterministic; the engine MUST sort keys before YAML emission:

- Top-level: `config_version` first, then group folders
  (lexicographic), then global resource keys (lexicographic).
- Within a group: resource keys in catalog order.
- Within a resource list: rows sorted by PK (composite PKs sorted
  lexicographically by their concatenated string form).

Two consecutive `arizuko export` invocations must produce
byte-identical YAML if the DB has not changed. Without this, file
hashing, git diffs, and "is anything different?" checks all break.

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

Full rebuild inserts all rows of all config tables in one transaction.
**Insertion order matters** with the v1 FKs: `groups` rows must be
inserted before `web_routes` and `route_tokens` rows that reference
them. The apply engine inserts in resource-catalog order, which
already places `groups` first.

For DELETE-before-INSERT (state-replacement) within a scope, the order
is reversed: children before parents. The two new FKs declare
`ON DELETE CASCADE`, so `DELETE FROM groups WHERE folder = ?` removes
the children automatically — explicit per-table DELETEs are also safe
(idempotent: rows already gone, no error).

If a future migration adds a config-to-config FK whose order is harder
to topologically sort (cycles, multi-step dependencies), set
`PRAGMA defer_foreign_keys=ON` at apply tx start — checks defer to
COMMIT, validated as a whole. For v1, neither cycle nor multi-step
dependency exists.

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

## Tokens are not in v1 manifests

Resources with **system-generated secrets as PK** (`invites.token`,
`route_tokens.token_hash`) are deliberately **excluded from v1
manifests**. A full rebuild would wipe live tokens; an upsert protocol
that preserves them requires either (a) putting secret values in YAML
or (b) a per-resource "name" indirection layer — both reintroduce
complexity disproportionate to the use case.

For v1, tokens are managed imperatively only:

- `arizuko invite issue <target_glob> [--max-uses N] [--expires <ts>]`
- `arizuko invite list`
- `arizuko invite revoke <token>`
- Same surface for `route_tokens` via `arizuko token …`.
- MCP tools: `invites.create`, `invites.revoke`, etc.

Imperative mutations still bump `config_version` and audit-log, so
operators can detect drift between manifest and live state, but
tokens themselves never appear in `manifest/`.

**Future work — v2 encrypted token export.** When demand surfaces, the
mechanism is:

1. Operator supplies an encryption key (file path or env var):
   `arizuko export invites --key /op/secrets/manifest.key > invites.yaml`
2. Export emits rows with tokens encrypted under that key:
   ```yaml
   invites:
     - token: 'enc:AES-GCM:<base64-ciphertext>'
       target_glob: krons/
       max_uses: 5
       expires_at: '2027-01-01T00:00:00Z'
   ```
3. Apply decrypts with the same key:
   `arizuko apply invites.yaml --key /op/secrets/manifest.key`
4. Tokens are PKs — upsert is straightforward (INSERT or UPDATE by token).
   No `name:` indirection, no `state:` field, same atomic rebuild as
   declarative resources.

The encryption key is operator-local, never committed. The YAML file
itself can live in git (ciphertext is opaque) or stay out of git
(operator preference). Re-applying without the key fails fast; the
ciphertext is useless without it.

This deferral is mechanical, not architectural. v1 ships without it
because most operators do not need git-tracked token state; v2 adds
~150 LOC (AES-GCM crypto + the export/apply paths) when needed.

## Splitting + composition

- `arizuko apply foo.yaml bar.yaml…` reads all files, merges, plans,
  applies as one run. `arizuko apply manifest/` reads every `*.yaml`
  in the directory the same way.
- Files compose **additively per resource** — two files contributing
  `routes: [...]` produce a union of rows.
- **Duplicate PK handling.** The PK for each resource is declared
  (see Resource catalog). Composition rules per PK:
  - Same PK, identical payload across files → silently deduplicated.
    `atlas.yaml` and `shared.yaml` may both declare
    `(user:alice, group:engineers)` in `acl_membership` without error.
  - Same PK, differing payload across files → parse-time error with
    location info: `"acl_membership PK (user:alice, group:engineers)
declared with conflicting fields in atlas.yaml:42 and
shared.yaml:17"`.
  - Same PK twice in one file → parse-time error regardless of
    payload (always a bug).
- No `include:` directives. Flat composition only.
- Order of file reads is deterministic (lexicographic by filename) so
  error messages are reproducible. But the resulting merged set is
  order-independent: composition is associative.

## Secret safety

Secret blobs **never** appear in:

1. Manifest YAML — only metadata (`scope_kind`, `scope_id`, `key`).
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
