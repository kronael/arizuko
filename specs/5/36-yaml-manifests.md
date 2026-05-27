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
| CAS version | `If-Match: <n>` header          | `config_version: <n>` arg     | `config_version:` manifest header |

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

## Optimistic locking (`config_version`)

`config_meta` is a single-row config-class table holding a monotonically
increasing integer: `config_version`. **Every** mutation increments it
on commit — apply, MCP, REST, direct DB writes. There is no untracked
write path.

**All three transports require CAS.** YAML carries `config_version:`
in the manifest header; REST sends `If-Match: <n>`; MCP tools take a
`config_version` arg. Mismatch → reject. The asymmetry tried in
earlier drafts (YAML only) is dropped — uniform CAS is simpler to
implement, simpler to reason about, and forces every writer to commit
to a base version.

Cost for MCP agents: one extra arg on every mutation tool, plus a
read-before-write step (call `get` to fetch current version, then
`create` with that version). This is a small tax that prevents
silent overwrites across all writer types.

**Export** stamps the current DB version into the manifest header:

```yaml
config_version: 42
```

**Apply** checks the DB version before writing:

- DB version == manifest `config_version` → proceed; increment to N+1
  on commit.
- DB version != manifest `config_version` → reject: "config changed
  since export; re-export or use --force to override."

A manifest with no `config_version` field is rejected in strict mode.
`--force` skips the check and writes unconditionally (last-writer-wins).

**Why CAS:** without it, two operators can both export at v42, both
edit, both apply with stale-but-superseded manifests, and the second
silently overwrites the first. The version forces a re-export step
that surfaces the conflict.

The pattern is identical to etcd's `revision`, S3's `If-Match` ETag, and
Kubernetes's `metadata.resourceVersion` — except simpler because we only
need it on the bulk-apply path.

### CAS implementation

Two mechanisms make the "every mutation bumps version" invariant real
without auditing every callsite:

**(1) AFTER triggers per config table.** One migration adds:

```sql
CREATE TABLE config_meta (version INTEGER NOT NULL DEFAULT 0);
INSERT INTO config_meta (version) VALUES (0);

-- For each config table: groups, acl, acl_membership, routes,
-- web_routes, scheduled_tasks, secrets, network_rules,
-- proxyd_routes, onboarding_gates:
CREATE TRIGGER <table>_bump_version
AFTER INSERT ON <table>
BEGIN UPDATE config_meta SET version = version + 1; END;
-- similarly for AFTER UPDATE and AFTER DELETE.
```

Every row mutation, from any callsite, bumps the counter. SQLite
triggers are tx-bound; if the tx rolls back, the bump rolls back too.

**(2) `BEGIN IMMEDIATE` for race-free CAS.** Apply must acquire the
RESERVED lock at tx start to serialize concurrent applies:

```
BEGIN IMMEDIATE;
  SELECT version FROM config_meta;
  -- compare to manifest config_version
  -- if mismatch (and not --force): ROLLBACK; reject
  DELETE FROM <config tables> WHERE folder IN (<manifest scope>);
  INSERT INTO <config tables> (...);
  -- triggers fire per row, version reaches N + (rows touched)
COMMIT;
```

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
1. Orphan recovery — scan manifest/ for `.<name>.yaml` files.
   For each orphan:
     - mtime(orphan) > mtime(<name>.yaml) OR <name>.yaml missing
       → promote: rename(orphan, <name>.yaml)
     - else → discard: delete orphan
2. Apply manifest/ — run `arizuko apply manifest/` in one tx
   (BEGIN IMMEDIATE; CAS check; DELETE + INSERT; COMMIT).
3. New-group filesystem prep — for each group folder in DB
   without an on-disk dir, call `container.SetupGroup(folder)`.
4. Open listen socket.
```

**Why orphan recovery first.** Mutation sync does rename POST-commit
(see Mutation sync). If the process died between COMMIT and rename,
the DB is one mutation ahead of `<name>.yaml`. Promoting the orphan
catches the YAML up before apply reads it. Apply-first would commit
the stale YAML state, undoing the just-committed mutation when the
orphan is later promoted.

**Why filesystem prep last.** SetupGroup is best-effort and idempotent
(`MkdirAll`). It writes prototype skills and chowns directories. Doing
it inside the DB tx would intermix file I/O with the apply commit; doing
it post-commit means the DB is authoritative and a partial filesystem
state can be retried via `arizuko repair` (idempotent re-run of step 3).

On `SIGHUP`, gated re-runs steps 1–3 (no socket re-bind). All daemons
see the new config on their next DB read — no signals to individual
daemons, no reload endpoints. SQLite WAL snapshot isolation lets
in-flight reads finish on the old config; subsequent reads see new.

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

When apply removes a `groups` row, runtime data referencing that folder
(messages, chats, audit_log, cost_log, …) **is not deleted**. The rows
stay in their tables with the now-orphaned `folder` string.

This is safe because arizuko uses **string references**, not declared
foreign keys. The only declared FK between any pair of tables is
`task_run_logs → scheduled_tasks` (ON DELETE CASCADE) — irrelevant
here. Removing a group:

- Frees the folder name for reuse.
- Strands runtime history under the old name (no longer surfaced via
  any group view).
- Does not corrupt the schema (no FK violation).

To fully erase a group:

```
arizuko group purge <folder>   # one command, several DELETEs + rmdir
```

This deletes runtime rows with `folder=<folder>`, drops the on-disk
directory, and clears any auxiliary state. `purge` is intentionally
imperative — it is destructive in a way YAML apply is not.

**`plan` output warns on group removal:**

```
$ arizuko plan
groups:
  - atlas    (REMOVE: 1247 message rows, 89 audit rows will be stranded;
              use `arizuko group purge atlas` to delete)
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

| Resource           | Apply mode | Owning daemon        | What it carries                                                         |
| ------------------ | ---------- | -------------------- | ----------------------------------------------------------------------- |
| `groups`           | rebuild    | gated                | folder registration + product + model                                   |
| `acl`              | rebuild    | gated                | unified ACL rules ([`../4/9`](../4/9-acl-unified.md))                   |
| `acl_membership`   | rebuild    | gated                | group membership for `group:` principals                                |
| `routes`           | rebuild    | gated                | message routing table                                                   |
| `web_routes`       | rebuild    | gated                | web-channel route bindings (webd JIDs)                                  |
| `scheduled_tasks`  | rebuild    | gated (timed reader) | cron entries                                                            |
| `secrets`          | rebuild    | gated                | metadata only — blob set out-of-band                                    |
| `network_rules`    | rebuild    | gated                | crackbox egress allowlist                                               |
| `proxyd_routes`    | rebuild    | proxyd               | reverse-proxy route table                                               |
| `onboarding_gates` | rebuild    | onbod                | per-instance onboarding policy                                          |
| `invites`          | —          | onbod                | not in v1 manifests — CLI/MCP only (see Tokens are not in v1 manifests) |
| `route_tokens`     | —          | gated                | not in v1 manifests — CLI/MCP only                                      |

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

`arizuko get <resource>` queries the live config DB and emits
the corresponding manifest YAML fragment.

**`get` round-trip rules.** `arizuko get <resource>` MUST emit
the exact YAML shape that `apply` accepts — no extra fields, no
omitted fields, no reordering of keys. Secret rows emit metadata
only (`scope`, `name`); the `value`/`ciphertext` field is never
present in `get` output regardless of caller scope.

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
