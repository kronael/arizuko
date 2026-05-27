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
proxyd routes, web routes, network rules, group registration.

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
      chat_jid: 'web:atlas'
      prompt: '/compact-memories episodes day'
      cron: '0 2 * * *'
      status: active

  secrets:
    - scope_kind: folder
      scope_id: atlas
      key: openai

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
      chat_jid: 'web:krons/eng/sre/oncall'
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

invites:
  - target_glob: 'krons/'
    max_uses: 5
    expires_at: '2027-01-01T00:00:00Z'
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

| Resource                                                                 | Home-file key                       | Home file                |
| ------------------------------------------------------------------------ | ----------------------------------- | ------------------------ |
| group config fields                                                      | the namespace key itself            | `manifest/<folder>.yaml` |
| `acl`                                                                    | folder extracted from `scope`       | `manifest/<folder>.yaml` |
| `acl_membership`                                                         | containing group key in document    | `manifest/<folder>.yaml` |
| `routes`                                                                 | `target`                            | `manifest/<folder>.yaml` |
| `route_tokens`                                                           | `owner_folder`                      | `manifest/<folder>.yaml` |
| `web_routes`                                                             | `folder`                            | `manifest/<folder>.yaml` |
| `scheduled_tasks`                                                        | folder extracted from `chat_jid`    | `manifest/<folder>.yaml` |
| `secrets`                                                                | `scope_id` when `scope_kind=folder` | `manifest/<folder>.yaml` |
| `network_rules` (group-scoped)                                           | `folder`                            | `manifest/<folder>.yaml` |
| `proxyd_routes`, `onboarding_gates`, `network_rules` (global), `invites` | — (base)                            | `manifest/base.yaml`     |

Protocol per mutation:

```
1. determine home file: manifest/atlas.yaml
2. read current home file → merge in the new/updated/deleted row
3. serialize merged state → write to manifest/.atlas.yaml
4. BEGIN tx
5.   write row to DB
6.   rename(manifest/.atlas.yaml, manifest/atlas.yaml)  ← atomic
7. COMMIT
   on step 6 failure: ROLLBACK, delete manifest/.atlas.yaml
```

`rename(2)` precedes commit. Invariant: **YAML is never behind DB.**
If the process dies between rename and commit, YAML is ahead — apply
on next startup reconciles forward. DB being ahead of YAML cannot happen.

No background goroutines. Sync is on the handler's critical path.

## Optimistic locking (`config_version`)

`config_meta` is a single-row config-class table holding a monotonically
increasing integer: `config_version`. Every apply increments it on commit.

**Export** stamps the current version into the manifest header:

```yaml
config_version: 42
```

**Apply** checks the DB version before writing:

- DB version == manifest version → proceed, increment on commit.
- DB version != manifest version → reject: "config changed since export;
  re-export or use --force to override."

This surfaces conflicts when two operators export + edit simultaneously.
`--force` skips the check and writes unconditionally.

MCP mutations increment `config_version` on each commit (same mechanism).
The version is a logical clock, not a timestamp.

## Startup apply + reload

On startup, `gated` runs `apply manifest/` against the live DB before
opening its listen socket. This makes the first request always see the
manifest's intent.

On `SIGHUP`, `gated` re-runs `apply manifest/` in one transaction.
All daemons see the new config on their next DB read — no signals to
individual daemons, no reload endpoints.

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
(`proxyd_routes`, `onboarding_gates`, `network_rules`, `invites`)
appear as top-level resource-kind keys with no group wrapper.

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
<scope_kind>/<scope_id>/<key> <value>`, never in YAML.
Schema: `(scope_kind, scope_id, key)` where `scope_kind` is `folder`
or `user`:

```yaml
atlas:
  secrets:
    - scope_kind: folder
      scope_id: atlas
      key: openai
```

**`product`** is a type tag (`assistant`, `oracle`, …) stored on the
group row. It labels what kind of agent the group runs. It does
**not** trigger re-seeding of group directory files on apply — the
prototype copy (skills, PERSONA.md, .claude/) happens once at group
creation via `container.SetupGroup`. Changing `product` in the
manifest only updates the DB column; it does not overlay files.

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

6. **JID fields as structured objects.** `chat_jid` and `match`
   values in `scheduled_tasks` and `routes` are currently opaque
   strings (`platform:kind/id`, `room=glob verb=glob`). Structured
   YAML objects would be more readable and less error-prone. Open:
   decide whether to expose the string form (current core.JID
   wire format) or a parsed `{platform, kind, id}` shape in v1.
   String form is simpler; structured form enables validation at
   parse time.

7. **`seq` on routes.** All current routes are inserted with
   `seq=0`; ordering falls back to `id ASC`. Seq may be removed
   when/if match specificity (longer match string = higher
   priority) replaces explicit ordering.

8. **`invites` and `route_tokens` class membership.** Both have
   auto-generated PKs (`token`, `token_hash`) and operator-issued
   state that should survive apply. Full-rebuild wipes them.
   Likely these belong in the runtime class, not config. Defer
   to implementation — remove from the config table list if the
   apply tool cannot round-trip them without destroying live tokens.

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
