---
status: partial
shipped: 2026-06-14
depends: specs/5/16-mcp-rest-unification.md, specs/5/17-openapi-mcp.md, specs/5/P-runed.md, specs/9/2-data-model.md
---

# specs/5/8 — YAML manifests + the full-instance archive

> **DECISION.** The SQLite DB is authoritative. YAML manifests are a
> transport dump/import — `pg_dump` / `pg_restore` for the cold tier — not
> a continuously-synced source of truth. No DB→YAML sync, no startup-apply,
> no SIGHUP-reload. A dump never claims to be live, so "drift" is a
> non-concept. `specs/9/3-git-as-truth.md`'s continuously-synced
> cold-tier-config is superseded; committing an `export` dump to git is fine
> (9/3 itself is unedited — read its `agents.toml` references through this
> lens).

> **DECISION (2026-08-04, operator).** Import must transfer _everything_:
> cold-tier config, `groups/<folder>/` filesystem trees, message history,
> and encrypted secret blobs — a full instance backup/restore, not only
> config. This redesigns the CAS mechanism (content hash, not a counter),
> the atomicity model (per-subsystem transactions + cross-subsystem
> pre-image rollback, not a cross-DB all-or-nothing), and adds the archive
> as a superset artifact around the existing manifest. None of this is
> built yet — see "Status" below for what ships first.

## Status: engine shipped, archive not designed until now

The resreg engine and the YAML config-manifest format ship
(`resreg/engine.go`, `resreg/resources/*.go`, 13 registered resources). The
CLI does **not** reach a production instance: `arizuko apply`/`plan`/
`export`/`get` all call `store.Open(dataDir+"/store")`
(`cmd/arizuko/apply.go:45,94,128,204`), which opens the frozen pre-split
`messages.db` (`store/store.go:51`) — not `routd.db` / `onbod.db`. Verified
2026-08-04, unchanged since the last check. **Concrete first step:**
repoint those four sites to `store.OpenRoutd`/`store.OpenOnbod`
(`store/store.go:76,103`) per [`5/16`](16-mcp-rest-unification.md)'s
owner-DB map. Tracked in `BUGS.md` Y1.

Everything below the config manifest — the archive container, message
history, secret-blob transfer, group filesystem trees, and the
backup-as-a-run mechanism in `runed` — is a **new design**, zero code
today. It supersedes the `config_meta`/counter CAS design and the
cross-DB "mixed-success/exit 3/rerun-skips" atomicity model this spec
previously specified (both flagged unbuildable-as-specced in BUGS.md Y1,
2026-08-02); read this file, not that history, as current.

## Why

`9/2`'s cold/warm/hot boundary left `agents.toml` unspecified. The config
manifest carries an instance's cold-tier config: ACL, routes, secrets
metadata, scheduled tasks, proxyd routes, web routes, network rules, group
registration. Operators additionally need to move a _whole instance_ —
config plus the data that makes it useful: what agents said, what they
remember, what credentials they hold. That's the archive.

## Two artifacts, one mechanism — not two specs

The archive is a **superset that contains the config manifest**, not a
bigger manifest. Concretely: `arizuko archive export` calls the exact same
`resreg.Export`/`EmitYAML` renderer `arizuko export` calls, and writes its
output as one of the archive's several documents. One renderer, two sinks
— manifest alone, or manifest inside an archive.

This spec stays one file because the two share the load-bearing mechanism
(content-hash CAS, per-subsystem transactions, the canonical renderer) at
the config layer, and because the archive literally embeds the manifest —
a reader of "archive" must read "manifest" regardless of file boundaries.
One piece does NOT belong here: the `runed` admission/execution change
that lets a filesystem restore claim a folder's run slot (below,
"Filesystem restore claims the folder's run slot") is general execution-
plane machinery whose natural long-term home is
[`5/P-runed.md`](P-runed.md) — any future folder-exclusive job (skill
sync, vacuum, migration) is another consumer of it, not another archive
feature. It's specified here because the need originates here; move the
prose to `5/P` when it ships, don't duplicate it there now.

## Surface

Four config verbs, unchanged, all in `cmd/arizuko/apply.go`:

- `arizuko export <instance> [file]` — dump cold-tier config to YAML.
- `arizuko apply <instance> <file> [--force]` — restore config: validate,
  rebuild.
- `arizuko get <instance> <resource>` — a scoped `export` of live state.
- `arizuko plan <instance> <file>` — non-mutating diff vs live state.

Two new archive verbs (unbuilt — CLI names, not yet in `cmd/arizuko/`):

- `arizuko archive export <instance> [dir_or_file] [--quiesced] [--since <RFC3339>]`
  — full backup: config + secret values + message history + group
  filesystem trees.
- `arizuko archive apply <instance> <archive> [--force]` — full restore.

**Rebuild scope is the load-bearing decision for config.** Per resource,
DELETE+INSERT is scoped to the folders the manifest mentions (`DELETE …
WHERE folder IN (<manifest scope>)`). A row's omission deletes it **only
within a mentioned scope**; groups absent from the manifest are untouched.
Instance-global resources rebuild wholesale. This is why `--prune` /
`state: absent` were cut — absence within a named scope already means
"remove". The archive's non-config subsystems (messages, secrets, group
trees) do not rebuild this way at all — see "The full-instance archive".

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
resolves each resource name to its owning DB at dispatch, so a future
daemon split leaves manifests valid.

**Multi-document YAML, one `---` document per subsystem** (a subsystem =
one owner DB's config projection — `routd`, `onbod`). Exported to a
directory it is one file each (`routd.yaml`, `onbod.yaml`); to a single
path they concatenate as `---`-separated documents; `apply` accepts either
because they are the same documents. No `--single` flag, no second
format.

## Three transports, one row schema

REST, MCP, and YAML are three transports over the **same row schema**,
defined once as `resreg.Resource` and reused by all three. Only the **row
fields** belong to the resource; verb, identity, batching, and version are
transport envelopes. YAML's envelope is: DROP+INSERT for the verb, YAML
nesting for identity, many-rows-one-tx for batching, a content hash for
CAS (below).

`5/17` owns the transport half (`Name`, `Endpoints`, `MCPDoc`, `Gate`,
`Handler`, `Store`). **This spec is authoritative for the row-schema half**
the engine adds — `RowType`, `Table`, `PKFields`, `Scope`, `Hooks`,
`SkipApplyRebuild` (`resreg/resreg.go:204`).

The archive's non-config documents (secrets-with-values, message history,
group filesystem trees) do NOT ride this mechanism — they carry data no
`resreg.Resource` addresses (unbounded event rows, encrypted blob values,
filesystem trees). Forcing them through the row-schema engine would bloat
one mechanism to serve two very different shapes (bounded declarative
config vs. unbounded immutable log vs. a directory tree); see "The
full-instance archive" for what each actually is.

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

## Content-hash CAS, not a counter

**DECISION (2026-08-04, operator): the CAS check is a content hash of the
canonical row projection, not a per-DB counter.** Deletes the entire
`config_meta` table design, `resreg.ConfigVersion`, and the "every writer
advances the version" rule this spec previously specified.

Mechanism: each subsystem's manifest-visible rows are canonically
serialized — the SAME `resreg.Export`/`EmitYAML` renderer that produces
the manifest, minus the CAS field itself — and hashed. `export` stamps the
result as `checksum: sha256:<hex>` in the document; `apply` recomputes the
hash from the **live DB**, in the same transaction that will write, and
refuses on mismatch unless `--force`.

This is a different value from the existing `ManifestDigest`
(`cmd/arizuko/apply.go`'s `sha256.Sum256(data)`, the raw YAML _file_
bytes, used only for the audit row's forensic correlation) — do not
conflate them. The checksum hashes the canonical DB-side projection; the
digest hashes whatever bytes the operator handed `apply`, edits included.

Why a hash over a counter:

- It answers the actual question — "is the DB still what I exported
  from?" — directly, instead of via a proxy that can be forgotten. A
  counter requires **every** writer (resreg-shared and any direct-SQL
  path) to remember to bump it in the same tx; a hash requires nothing
  from writers because it's computed fresh at read time.
- It catches a manual `sqlite3` edit. A counter doesn't move unless the
  editor knows to bump it.
- It's per-owner-DB for free, with no home-finding problem. `config_meta`
  never existed in any split owner DB (BUGS.md Y1 point 2, verified
  2026-08-02 against all three live instances) — building it now would be
  new schema with no other consumer. A hash needs no table at all.
- It makes the `secrets`-excluded-from-the-counter carve-out
  unnecessary: a rotated blob was excluded from the OLD counter because
  routine rotation shouldn't invalidate a pending apply. A hash is
  computed over the manifest projection, and `secrets` values are already
  outside that projection (`SkipApplyRebuild`, "Secret safety" below) — so
  rotation never touches the hash in the first place. Nothing to carve
  out.

Determinism is already proven: "Round-trip honesty" (below) requires two
consecutive exports of an unchanged DB to be byte-identical. The hash
reuses that guarantee; it needs no new determinism work.

**Reused by the pre-image rollback mechanism** (next section) as the
pre-image _and_ the trigger for whether a re-apply is a no-op — a stale
hash with an unchanged projection reads as "already applied," not as a
conflict.

## Cross-subsystem apply: per-subsystem transaction, pre-image rollback

**DECISION (2026-08-04, operator, superseding this spec's prior
mixed-success/exit-3/rerun-skip design — see BUGS.md Y1 "Recommended
answers," now stale).**

Within one subsystem there is nothing to compensate: one subsystem = one
owner DB = one SQLite `BEGIN IMMEDIATE … COMMIT`. CAS check, scoped
DELETE+INSERT, secrets/tokens UPSERT (see below), commit or rollback —
native SQLite transaction semantics, no extra machinery.

Across subsystems (`routd`, `onbod` — the only two owner DBs with
`resreg` resources today, per `5/16`'s owner-DB map): before writing,
`apply` exports the **current** state of every subsystem it is about to
touch into a rollback set — the same `resreg.Export`/`EmitYAML` call the
CAS hash already uses, so this reuses the renderer as the pre-image
producer instead of inventing a second backup format. It then applies
each subsystem in turn. If a later subsystem's transaction fails after an
earlier one committed, the already-committed subsystem(s) are restored by
re-applying their pre-images (the SAME `apply` codepath, fed the exported
snapshot instead of the operator's manifest).

**Load-bearing constraint: the rollback restores the manifest
projection, not the database.** A whole-DB restore (a raw file swap) would
discard messages that arrived, and memory a turn wrote, during the apply
window — live traffic the apply never touched. Because `resreg.Export`'s
projection is exactly the config rows (the cold-tier resource set), and
because a subsystem's config transaction is scoped-DELETE+INSERT (never a
table truncate), re-applying a pre-image touches only what the forward
apply touched. This is what makes rollback safe _here_, and it is exactly
why rollback does NOT extend to message history or filesystem trees below
— they are not "the manifest projection," so this rollback mechanism has
no opinion about them; each has its own recovery story (idempotent
append; best-effort, stated as such).

This deletes the old design's premises along with it: no preflight-all-
owners pass, no deterministic commit order beyond catalog order, no exit
code 3, no "rerun skips the applied owner." A failed cross-subsystem apply
ends with every subsystem back at its pre-apply content hash; a clean
re-run is just `apply` again.

## Consistency levels — an archive is a smear, and must say so

A full-instance archive taken from a running system is not a snapshot; it
is a smear across time, from four concrete sources:

1. **No cross-DB transaction.** `routd.db`, `onbod.db`, `auth.db`,
   `runed.db` are separate SQLite files (`store.OpenRoutd`/`OpenOnbod`,
   `authd/db.go`, `runed/db.go`) — export reads them in sequence, so a
   write landing between two reads appears in one document and not
   another. `5/31`'s minimality pass sidestepped exactly this seam for
   pairing by putting the token in `route_tokens` so token and edge share
   one transaction (`routd/migrations/0026-route-token-kind.sql`); an
   archive spanning DBs cannot do the same trick — there's no single
   transaction to put everything in.
2. **DB vs. filesystem.** The `groups` row and `groups/<folder>/` are
   separate substrates (`container.SetupGroup`, `container/runner.go:964`,
   writes the tree; the row lives in `routd.db`). An agent writing
   `MEMORY.md` mid-export can yield a config row pointing at a
   half-written tree.
3. **Live turns.** Messages, tool-call audit rows, and memory are written
   over seconds by an in-flight turn; export mid-turn captures half a
   turn.
4. **`SECRETS_KEY` is deliberately absent** from the archive (below) — a
   correctness constraint, not a consistency one, but it compounds:
   ciphertext travels, the key that makes it meaningful does not.

Verified empirically against this codebase (not assumed): `VACUUM INTO`
and, more directly, a single explicit read transaction (`BeginTx(ctx,
&sql.TxOptions{ReadOnly: true})`) both give a consistent MVCC snapshot
under WAL while a concurrent connection commits new rows — confirmed with
`modernc.org/sqlite` (the driver this codebase uses, `store/store.go:13`,
`go.mod`), inserting into two tables from a second connection mid-read-tx
and confirming the read transaction's later `SELECT`s neither saw the new
rows nor changed the count already read. This is the same primitive
`VACUUM INTO` uses internally; the row-level export path doesn't need a
file-level copy, only the cheaper form — one `BEGIN` wrapping the several
`ScanAll` calls `resreg.Export` already makes, which is not how `Export`
works today (each `ScanAll` is presently its own implicit autocommit read
— a real gap the archive work should close: wrap `Export`'s whole loop,
plus the messages/secrets reads for that subsystem, in one explicit read
transaction per owner DB).

Three levels, and the archive must **declare which one produced it**
rather than imply an image it doesn't have:

- **`live`** (default). Per-subsystem — each document internally
  consistent via its own one read transaction — the archive as a whole
  not. No downtime. For cold-tier config this is nearly indistinguishable
  from a true snapshot because config changes are human-paced; for
  messages and filesystem trees the smear is real but bounded to the
  export's wall-clock duration.
- **`quiesced`** (`--quiesced` flag). The operator has already stopped the
  instance (`systemctl stop arizuko_<instance>`) before running `archive
export`; the flag only stamps `consistency: quiesced` in the archive's
  metadata — the tool does not stop/start services itself. Trivially
  consistent, costs downtime, the only level that is honestly
  point-in-time. What a real DR restore wants.
- **Pre-flight validation at restore** (always, regardless of export
  level): before any subsystem's transaction opens, `apply` checks
  referential integrity across the archive's documents (the missing-group
  rule, below) and refuses the whole restore rather than importing a
  half-wired instance. This is orthogonal to the smear question — it
  catches gross errors (a route naming an absent group), not staleness.

`archive.yaml` (the archive's top-level index, alongside the subsystem
documents) records `consistency: live|quiesced` and, per subsystem, the
read transaction's start timestamp. `archive apply` reads this and is
free to refuse a `live` archive for a use case that demands a true
point-in-time image (that policy is an operator call, not specified here
— the archive just tells the truth about what it is).

The general principle, already proven twice in this codebase: co-locate
what must be atomic (token + edge in one table, one subsystem's config in
one transaction); where you cannot, name the seam and validate at the far
end rather than pretending it isn't there.

## The full-instance archive

### Shape

A **directory** of per-subsystem artifacts, tarred as a whole for
transport — same units whether it lands as a directory or a single tar,
matching the multi-document YAML decision's own directory-vs-single-file
duality:

```
archive.yaml            # consistency level, per-subsystem snapshot timestamps
routd.yaml               # config manifest document (this spec's existing mechanism)
onbod.yaml                # config manifest document
routd.secrets.yaml       # secret + route-token/invite VALUES (see below) — separate
                          # from routd.yaml so a plain `export` reader is never handed one
routd.messages.jl        # message history, JSONL (repo convention: .jl, not .jsonl)
groups.tar               # groups/<folder>/ trees, one entry per folder
```

`routd.yaml`/`onbod.yaml` are byte-identical to what `arizuko export`
would produce standalone — literally the same function call, written to a
different path. Everything else is new.

Not archived, and why: `auth.db` (identities, refresh tokens, signing
keys) and `runed.db` (spawn/session execution history) are outside the
operator's stated scope ("config + group filesystem trees + message
history + encrypted secret blobs") and outside it for a reason, not by
oversight — `auth.db` is authentication infrastructure whose signing keys
rotate and whose refresh tokens shouldn't be revived stale; `runed.db` is
purely hot-tier execution history (`9/2`: "operational state... SQLite
only, rebuildable"), with no cold config content and no data an operator
would want back verbatim. Restoring either onto a running instance would
be actively wrong, not merely out of scope.

### Message history

**Full, always — no time window, no opt-in flag.** The operator's own
framing ("message history" as part of "everything," "a full instance
backup/restore") settles this; a partial-by-default archive would silently
fail the one property the operator asked for. `--since <RFC3339>`
narrows `archive export` for operators who want cheaper incrementals
(krons has real volume), but the unqualified command is the full table.

Format: JSONL (`.jl`), not YAML. `messages` is unbounded and event-shaped
(`9/2`: "no cold tier... warm for the content, hot for queue position and
delivery state") — the row-schema engine's `Export` loads a resource's
rows fully into memory as a `[]RowType` and builds a `yaml.Node` tree
(`resreg/engine.go`), a shape built for tens-to-hundreds of small
config rows, not hundreds of thousands of messages. Reusing it here would
bloat one mechanism to serve two very different cardinalities; a
streaming JSONL writer (one `SELECT`, one row per line) is the right tool
and needs none of the row-schema engine's CAS/diff/rollback machinery,
because none of it applies to an append-only table.

**Import semantics: idempotent bulk append, not rebuild.** `messages.id`
is the PK; import is `INSERT OR IGNORE` (or equivalent) keyed on it, so
re-running the same archive, or an overlapping `--since` window, is a
no-op on already-present rows. No CAS, no rollback — the "manifest
projection, not the database" boundary above states plainly that rollback
covers config only; messages were never in that projection, so this
isn't a gap in the rollback design, it's outside its stated scope. Import
runs in batches (not one giant transaction spanning the whole history) so
a multi-hour transfer doesn't hold `routd.db`'s write lock for its
duration — a real concern specifically when restoring onto a populated,
live instance (below).

**`turn_id`/`chat_jid` continuity:** both are plain `TEXT` columns on
`messages` (`routd/migrations/0001-initial-schema.sql:27-46`), carried
verbatim by the bulk import — no rewriting, no cross-reference to resolve,
because nothing else in `routd.db` FK-references them (`chat_jid` has no
declared FK to `chats.jid`).

**FTS index rebuild: none needed, by construction.** `messages_fts` is a
trigger-maintained shadow (`store/migrations/0070-messages-fts.sql`):
`messages_fts_ai` fires `AFTER INSERT ON messages`. As the bulk import
INSERTs rows through the normal `messages` table (not a raw file copy),
the existing trigger populates the FTS shadow incrementally, per row, for
free — the same mechanism migration 0070 already relies on for live
traffic. No separate "rebuild the index" step, no new code.

`chats` rows (hot tier — `agent_cursor`, `sticky_group`, `sticky_topic`)
are deliberately **not** part of this artifact. They're rebuildable
operational state (`9/2`), not history; a restored instance starts with
whatever `chats` rows already exist (or none, on a fresh instance) and
repopulates them as new messages route through, same as today.

### Secret and token values — the "as-is" lane

**DECISION (2026-08-04, operator): encrypted secret blobs travel as-is.
`SECRETS_KEY` never enters the archive. Import fails loud if the key on
the target cannot decrypt.**

The stored form is already a plain string — `"v2:" + base64(nonce ||
ciphertext)` (`store/secrets.go:21,35`) — trivially a YAML scalar, no new
encoding needed. `routd.secrets.yaml` carries a row shape identical to the
existing `SecretsRow` (`resreg/resources/secrets.go`) plus the `Value`
field the config manifest deliberately omits ("Secret safety," below,
unchanged for plain `export`). Row count here is small (per-folder/user
secrets, not an unbounded table), so YAML is the right fit, unlike
messages.

Import is **UPSERT by `(scope_kind, scope_id, key)`, never DELETE+INSERT**
— `secrets` keeps `SkipApplyRebuild = true` even inside the archive path;
this decision only adds a value to what was previously metadata-only, it
does not change the never-wipe-by-omission invariant that flag exists to
enforce (`resreg/resources/secrets.go`'s own doc comment already
anticipated this: "Rebuilding triples from YAML on apply... is drafted").
The same lane covers `route_tokens`/`invites` hash-at-rest values — a
generalization of this decision, not a new one: system-generated
credential material (secret blobs, token hashes) travels as-is through an
UPSERT lane, never through config's DELETE+INSERT+CAS lane, for the same
reason "Tokens in manifests" (below) already forbids rebuilding them from
an operator-edited manifest. Ordinary config `apply` is unaffected — it
still never touches these tables' credential columns; only `archive
apply` does, via this dedicated lane.

**Fails loud, using existing code, not new crypto.** `store.open()`
(`store/secrets.go:41`) already tries every key in the configured keyring
(`s.secretKeys`, populated from `core.SecretKeyring`'s comma-separated
`SECRETS_KEY`) and returns an error if none authenticate — AES-GCM's tag
check makes this a wrong-key detector for free. `archive apply` calls the
same function on one row before writing anything for this subsystem, and
refuses the whole secrets step (not the whole archive — config and
messages proceed independently) if it fails. This needs `store.open` (or
an equivalent one-row decrypt-check) reachable from the archive CLI —
currently package-private; exporting it is a small, mechanical change,
not new logic.

### Group filesystem trees

`groups/<folder>/` holds `PERSONA.md`, `skills/`, `.claude/` (session
state), `MEMORY.md`, `.diary/`, `media/`, prototype content — everything
`container.SetupGroup` (`container/runner.go:964`) writes when
provisioning a group, plus everything the agent writes at runtime.

**Carried: the whole tree, verbatim, as `groups.tar`.** **Regenerated:
nothing**, for a folder the archive actually has a tar entry for.
`SetupGroup`'s job — `mkdir`, prototype copy, `.claude/skills` seeding,
`chownR` — is fresh-provisioning behavior; a restored folder with real
archived content is not fresh, and re-running that scaffold over it would
silently overwrite `MEMORY.md`/`PERSONA.md`/skills with prototype
defaults. `archive apply` skips the scaffold step for any folder present
in `groups.tar` and extracts the tar in its place. **`onbod`'s
`SetupGroup` stays the only provisioning code path** (root `CLAUDE.md`:
"no parallel second path") — the existing "Apply is a restore, so
filesystem prep follows the commit" section's rule (call `SetupGroup` for
groups rows "lacking a complete on-disk dir") is unchanged and remains
the fallback for a manifest-only apply, or for a folder the config
manifest names but the archive's `groups.tar` doesn't cover (a narrower
or older archive). Tar extraction is layered strictly _after_ that rule,
never instead of it.

Ordering, extending the existing "filesystem prep follows the commit"
rule: config subsystem commits first (so every `groups` row exists), then
filesystem restore proceeds per folder — which is also where the run-slot
claim below applies, since extraction touches a live agent's own working
directory.

### Filesystem restore claims the folder's run slot

Extracting a tar is not atomic, and a live agent can hold
`groups/<folder>/` open — writing `MEMORY.md` mid-restore is a real
collision, not a hypothetical. The question is how to keep a turn from
starting _during_ that folder's extraction, and the answer is: **don't
build a new lock. A filesystem restore runs as a run in the folder,
claiming the folder's existing spawn slot.**

Verified against the code, not assumed: `runed.Manager.Run`
(`runed/manager.go:92`) is already a claim-or-reject executor. The
admission decision and spawn-row creation are one atomic critical section
under `m.mu`, with `m.db.GetActiveSpawn(folder)` as the exclusion
(`manager.go:109`) — two spawns for one folder are impossible by
construction, verified: this is the exact mechanism `spawns` documents as
"per-folder serialization (one live spawn per folder)"
(`manager.go:24`). `spawns.container_name` is `TEXT NOT NULL`
(`runed/migrations/0001-initial-schema.sql:22`) but carries no `CHECK` —
`''` is a valid non-null value, so a containerless run needs no schema
relaxation.

Claiming that slot for a filesystem restore inherits four properties
instead of building them:

- **Exclusion.** No agent turn can start while the restore holds the
  folder — same mechanism that already stops two agent turns from
  overlapping.
- **Backpressure.** A message arriving mid-restore finds `GetActiveSpawn`
  non-nil and `m.steerFns[folder]` nil (a containerless run registers no
  steer callback), so `Run` returns `Busy: true` and routd re-feeds from
  its own DB-backed dispatch queue (`manager.go:112-125`) — nothing lost,
  no new queueing logic.
- **Wedge protection.** The existing `RunTTL` kill-deadline
  (`manager.go`'s `spawn()`, "enforce runTTL as a kill-deadline") already
  kills a run that overstays — the entire reason a separate lease/TTL
  mechanism was floated and rejected.
- **Visibility.** It appears in `spawns`, so `dashd`'s runed page and the
  operator already see it (`dashd/runed_page.go:54-90`).

**This requires real, scoped code changes in `runed` — it is not free
today.** Naming them precisely so "claim the slot" isn't hand-waved:

- **`spawns.kind TEXT NOT NULL DEFAULT 'agent'`** — a new migration
  (`runed/migrations/000N-spawn-kind.sql`), the same idiom
  `route_tokens.kind` shipped for pairing
  (`routd/migrations/0026-route-token-kind.sql`: `'route'`/`'pair'`,
  kind-scoped resolution both directions). `Spawn`/`RunRequest`/`RunSpec`
  gain a `Kind` field alongside the existing `Isolated`/`Elevated`
  precedent (`runed/api/v1/types.go:31,36`) — not a new pattern, the same
  one. Verified no breakage to existing readers: `CreateSpawn`'s `INSERT`
  uses an explicit column list (`runed/db.go:185-187`), and every direct
  `spawns` reader outside `runed` itself (`dashd/runed_page.go`'s two
  queries) also uses explicit column lists — adding a column changes
  neither. `routd` never reads `spawns` directly (confirmed by grep: only
  `runed/manager.go`, `runed/db.go`, `dashd/runed_page.go` reference the
  table); it talks to `runed` over the `RunRequest`/`RunOutcome` JSON
  contract, so it needs no change to keep working with an implicit
  `kind='agent'`.
- **`Manager.Run`'s post-claim step becomes a dispatch by `kind`, not a
  hardcoded container spawn.** Everything up to and including the claim
  (breaker check, `m.mu.Lock()`, `GetActiveSpawn`, claim/steer/busy,
  `manager.go:96-159`) is unchanged. What changes is what runs after: today
  `spawn()` unconditionally calls `m.runtime.Run(ctx, RunSpec{...})`
  (`manager.go:186`); it becomes a lookup into a `map[kind]Executor`,
  where `Executor` is literally the existing `Runtime` interface
  (`Run(ctx, spec) RunResult`, `Kill(containerName) error` —
  `runed/runtime.go:68-74`) — no new interface. `kind='agent'`'s executor
  is today's `container.DockerRunner`-backed `Runtime`, unchanged;
  `kind='backup'`'s executor is a new, small implementation of the same
  two methods that extracts `groups.tar`'s entry for the folder instead
  of spawning Docker. `Kill`/`StopFolder` need the same lookup-by-kind (a
  spawn row's recorded `kind` selects which executor's `Kill` to call) —
  currently both call `m.runtime.Kill` unconditionally
  (`manager.go:277,306`).
- **Breaker accounting scopes to `kind='agent'`.**
  `GetFailures`/`IncrFailures`/`ResetFailures` (`manager.go:98-100,283-291`)
  are keyed on folder with no kind awareness today — a failed backup would
  otherwise count against the agent and could stop it spawning. `endRun`
  must skip breaker accounting entirely for non-agent kinds.
  `spawns.state`/`outcome`/`exit_code` still record the backup's own
  success/failure (visibility is unaffected), only the breaker's
  interpretation of failure changes.
- **The busy-branch steer attempt must skip non-agent kinds.** Today, when
  `active != nil`, `Run` unconditionally tries `steer(req.MessageBatch)`
  if a steer callback is registered (`manager.go:112-118`) — correct for a
  second agent message arriving while the first is mid-turn, wrong for a
  backup request, which carries no message and must never be steered into
  a live agent's running container. This is a real bug if the kind
  dispatch is built without it, not a hypothetical: it would inject an
  empty batch into whatever turn is live.
- **Wedge protection needs the deadline moved up one level to actually be
  uniform across kinds.** Today `RunTTL` is armed _inside_ the container
  Runtime ("the Runtime honors `RunTTL` from within the run path... so no
  detached manager timer races container creation" —
  `manager.go`'s `spawn()` doc comment) — each executor would otherwise
  need to reimplement it. Arming a `context.WithTimeout(ctx, m.runTTL)`
  at the shared dispatch site in `spawn()`, wrapping whichever executor
  runs, makes "wedge protection already exists" true for `kind='backup'`
  and every future kind, not just `kind='agent'`.
- **`dashd`'s runed page** hardcodes "Stop the agent currently working for
  %s" in its kill-confirm text (`dashd/runed_page.go`) — misleading for a
  non-agent kind. Small, independent fix: read `kind` and vary the label
  (or at minimum say "run" instead of "agent").

The payoff, stated because it's the actual reason to do this instead of a
bespoke lock: any future folder-exclusive job — a skill sync, a vacuum, a
migration — becomes a new `kind` with an executor, not a new mechanism
with its own locking. This is the fourth application of the same
principle in this spec: pairing's token-and-edge into one transaction
(`route_tokens.kind`), config rollback via the export renderer, and now
backup onto the run slot — don't add coordination beside an existing
serialization point, move onto it.

An earlier per-folder lease/TTL design and a per-folder "pause new
dispatch" flag were both considered and rejected in favor of this. Neither
is built. `groups.open` (`store/groups.go:238-260`) was checked as a
candidate "pause admission" lever and rejected as a red herring: it's read
_only_ by `dashd`'s admin page for cross-group sibling visibility
(spec 6/F), never consulted by any dispatch or admission path — the
`routd/db.go:329` comment calling it "ambient turn admission" is stale
prose, not evidence of behavior.

### The missing-group rule

**One rule: a manifest apply refuses, before writing anything, if any
row — in any subsystem document, through any reference shape (a declared
FK, a `Resource.Scope`, or a per-resource `Hooks.ValidateRow` check) —
names a folder that is not a `groups` row somewhere in the same subsystem
document or already live in the target DB.**

This closes a real gap, not a hypothetical one. Two folder references are
already real SQLite FKs and self-enforce today (`web_routes.folder`,
`route_tokens.owner_folder`, both `REFERENCES groups(folder) ON DELETE
CASCADE` — `routd/migrations/0001-initial-schema.sql:133,141`): apply's
own transaction would fail on a dangling reference for these two,
noisily, mid-write. But several folder-shaped references in this schema
are deliberately string-typed with no FK ("FK posture," below —
`acl.scope` glob, `routes.target` fragment, `scheduled_tasks.chat_jid`,
`secrets.scope_id`) precisely because they aren't column-equal to a
folder or are polymorphic — and SQLite catches none of those. Today an
operator's typo in one of those fields creates a silently orphaned row
instead of a validation error; a real DR restore that got a folder name
wrong somewhere would find out only much later, by absence, not at apply
time.

The check reuses the existing scope machinery rather than inventing a
parallel validator: for resources carrying a declared `Scope`,
`manifestScopes` (`resreg/engine.go:305`) already extracts every folder
the manifest touches for that resource — the same set already used to
pick scoped-DELETE targets. For string-typed references `Scope` cannot
capture (globs, fragments — the exact set "Scope cannot be inferred from
RowType" already names), the check is a per-resource `Hooks.ValidateRow`
— the existing, documented extension point for "validation beyond types"
(`resreg/engine.go:34-60`'s `Hooks` doc comment), not new mechanism.

This does not touch or reopen "Group removal semantics" below (a manifest
_silently omitting_ a group that already exists is a different, already-
decided case) — this rule is specifically about a reference to a folder
that was never a group at all.

### Restoring onto a populated instance

Config apply was never dangerous here — it's CAS-checked, scoped, and its
blast radius is exactly the rows the manifest mentions. The archive's
non-config subsystems have three different risk profiles, and a populated
target is where they diverge:

- **Config + secrets + tokens:** unchanged risk. CAS'd DELETE+INSERT for
  config; UPSERT (never delete) for secret/token values. Safe on a
  populated instance by the same reasoning as today.
- **Message history:** safe by construction — `INSERT OR IGNORE` never
  overwrites or deletes an existing row. Restoring an archive twice, or
  onto an instance that already has some of the same history, converges
  to the union.
- **Filesystem trees: the actual danger.** Extracting `groups.tar` onto a
  folder that already has a non-empty `groups/<folder>/` tree overwrites
  live content — whatever the current agent has written since the archive
  was taken. **Rule: `archive apply` refuses a folder's filesystem step
  (skips it, reports it, the folder's config/messages steps proceed
  regardless) when the target tree is non-empty, unless `--force`** — the
  same flag `apply --force` already uses to mean "override a built-in
  safety refusal," not a second flag for a second concept. A genuinely
  fresh/empty instance (the DR use case) needs no flag, because there's
  nothing to clobber; a populated target (re-running an old archive,
  merging instances) requires the operator to say so explicitly. The
  per-folder run-slot claim above still applies regardless of `--force` —
  it's about _not corrupting a tar mid-extraction while a turn is live_,
  a different hazard than _overwriting the tree's prior contents on
  purpose_.

### Cross-instance portability

What travels cleanly, and what doesn't, verified against the actual
columns rather than assumed:

- **`WEB_HOST` is never embedded in a row.** It's read from the
  environment at call sites (`core/config.go:191`,
  `container/runner.go:797`, `routd/route_tokens_resource.go:170` for
  pairing-link URLs) and composed into a URL only at read/render time. No
  rewrite needed on import; the archive carries no host to rewrite.
- **`proxyd_routes.backend`** is a compose service URL
  (`http://dashd:8080` style, `store/migrations/0050-proxyd-routes.sql:7`)
  — portable as long as the target instance uses the same
  `:8080`-per-daemon compose convention it always does (CLAUDE.md). No
  rewrite needed.
- **Channel JIDs are NOT rewritten and make the archive instance-
  specific.** `messages.chat_jid`, `route_tokens.jid`,
  `scheduled_tasks.chat_jid` are bound to the _source_ instance's channel
  credentials (a specific WhatsApp number, a specific Telegram bot token).
  Restoring onto a different instance leaves them syntactically valid but
  operationally inert — no FK enforces them, so nothing breaks, but a
  restored chat history won't resume routing until the same external
  accounts are reconnected on the target. This is a fact about the
  archive, not a bug to fix by rewriting — there is no correct rewrite
  target without operator input.
- **`SECRETS_KEY` never travels** (decision above) — a secrets subsystem
  is only meaningful on a target holding the same key(s); cross-instance
  secrets restore requires the operator to carry the key out-of-band.
- **Already-issued token/pairing URLs break** on cross-instance restore
  even though the token rows themselves import fine — the URL a recipient
  already has embeds the _source_ instance's `WEB_HOST`, baked in at mint
  time, not stored. Not a data problem; an operational one, same shape as
  the JID case.

## Apply is a restore, so filesystem prep follows the commit

Group filesystem state (skills, `.claude/`, prototype) is **eventually
consistent with the DB** — filesystem ops cannot join a SQLite tx. After
the config tx commits, `apply` calls `container.SetupGroup(folder)` for
every group row lacking a complete on-disk dir. A failed `SetupGroup`
surfaces as an apply error, never swallowed: a row without its dir makes
routing `docker run` against a missing path and exit 125. `arizuko repair`
re-runs the prep alone, idempotently. Direct `mkdir` of a group is
forbidden (CLAUDE.md). For `archive apply`, this rule is unchanged and
stays the fallback; a folder the archive actually has a `groups.tar`
entry for skips the scaffold and extracts the tar instead ("Group
filesystem trees," above).

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
`arizuko token …`); their mutations still write an audit row. Under the
content-hash CAS (above) they need no counter bump either — the hash is
recomputed fresh from live rows at the next export/apply, not advanced
incrementally, so a token mutation between two config applies is simply
reflected the next time either side reads the DB.

`archive apply` does not reopen this: its secrets/tokens UPSERT lane
("Secret and token values," above) is a _different_ codepath from config
`apply`'s DELETE+INSERT, added specifically because it never rebuilds —
it only ever adds or matches an existing row by PK. Ordinary `arizuko
apply` on a hand-edited manifest is completely unaffected by the archive
work and still never touches these columns.

<!-- VERIFIED 2026-08-02: the code only half-honours this. `route_tokens`
     sets SkipApplyRebuild (resreg/resources/route_tokens.go:111), but
     `invites` does NOT (resreg/resources/invites.go:36-44) — it carries a
     RowType with no skip, so Apply's loop (resreg/engine.go:523) would
     DELETE+INSERT live invite tokens, and Export emits the raw bearer.
     Inert only because the CLI still targets the frozen messages.db.
     `invites` needs SkipApplyRebuild before any export/apply ships.
     Also tracked as a 5/20 blocker. -->

Deferred: v2 encrypted token export (operator-supplied key, `token:
'enc:AES-GCM:<b64>'`) for the _config_ manifest specifically — superseded
in spirit by the archive's as-is UPSERT lane above, which now covers this
need for the full-backup case; the config-manifest-only path stays
metadata-only.

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
   The archive's message-history import is additive-only (`INSERT OR
IGNORE`), so it doesn't violate this rule; it was never a DELETE+INSERT
   consumer of the resreg engine to begin with.
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
non-deterministic): group folders lexicographically, then global resource
keys lexicographically; within a group, catalog order; within a list, rows
sorted by PK. Two consecutive exports must be byte-identical on an
unchanged DB or file hashing and git diffs break — and the content-hash
CAS above depends on exactly this guarantee holding.

## Secret safety

Secret blobs never appear in the **config manifest's** YAML (metadata
only), `plan` output (shown as set/unset), error payloads, or audit rows.
`secrets` carries `SkipApplyRebuild` (`resreg/resources/secrets.go:54`) so
config `apply` validates and diffs the metadata but never DELETE+INSERTs
— a rebuild would wipe the imperatively-set blob. Setting one outside the
archive path is a separate operator command, `arizuko secret set`. Trust
boundary unchanged from [`9/2` "Entity notes worth keeping"](../9/2-data-model.md#entity-notes-worth-keeping).

This guarantee is specifically about `arizuko export`/`apply`/`get`/`plan`
— the config manifest. `archive export`/`apply` is a **different,
narrower-audience artifact** (`routd.secrets.yaml`, above) that exists
precisely to carry the value, under the as-is/UPSERT rule decided for it.
An operator who only ever runs the config verbs never produces or
consumes a file containing a secret value; only `archive` does, and it
says so in its own filename.

Folder-scoped secrets infer the folder from group nesting and declare only
`key:`; user-scoped ones add an explicit `user:` — the same
implicit-from-nesting rule as `acl` and `scheduled_tasks`.

## Markdown vs YAML

**If it's a row, YAML. If it's a paragraph, Markdown.** YAML carries
table-shaped cold-tier rows (operator intent). Markdown carries prose
(`PERSONA.md`, `MEMORY.md`, `.diary/`, `skills/<name>/SKILL.md`,
`PRODUCT.md`) — agent context living in the group directory, never manifest
rows, never referenced from YAML, never content-hashed in the DB. The
archive's message history is neither — an unbounded immutable event log —
which is exactly why it gets its own JSONL lane instead of being forced
into one side of this rule.

## Status is not in the manifest

A dump carries cold-tier config rows only; live state is read by `arizuko
get`. Dumps never carry `status:` / `applied_at:` / `last_error:` — the
same spec/status boundary `kubectl` draws. `archive.yaml` is the one
exception by design — it exists specifically to record the archive's own
consistency level and snapshot timestamps ("Consistency levels," above),
because an archive that doesn't say how live it is cannot be restored
responsibly.

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
SET NULL. This same list is exactly what "The missing-group rule" above
must validate for the string-typed half, since SQLite's own FK enforcement
only covers the first paragraph.

**Insert in catalog order** so `groups` precedes its children; DELETE
reverses. No config-to-config cycle exists in v1; if one appears, set
`PRAGMA defer_foreign_keys=ON` at tx start.

## Non-goals

No live reload / file watcher / DB→YAML sync. No DAG dependency resolution
beyond catalog ordering. No web UI for manifests, no multi-instance apply,
no transactional cross-daemon rollback of anything wider than the config
projection (above). Imperative `arizuko group add` verbs stay for ad-hoc
work — manifests are the declarative path, not a replacement.

Concluded, not merely deferred, should NOT be built:

- **A generic streaming/row-schema engine extension for unbounded
  tables.** Message history's JSONL lane is deliberately outside
  `resreg` — teaching the row-schema engine to stream would serve exactly
  one consumer (`messages`) at real complexity cost (CAS/diff/rollback
  semantics that don't apply to an append-only log). Not worth it.
- **A per-folder lease/TTL or "pause new dispatch" flag for filesystem
  restore.** Rejected in favor of claiming the existing run slot (above)
  — building a second serialization mechanism next to one that already
  does the job is exactly the anti-pattern this spec keeps naming and
  avoiding.
- **`auth.db`/`runed.db` in the archive.** Out of scope on purpose (above)
  — not an oversight to fix later.
- **A raw whole-DB-file snapshot (`VACUUM INTO`) as the transport for any
  archive document.** Verified usable under WAL with live writers (above),
  but not needed: the row-level export path only needs one explicit read
  transaction per owner DB for snapshot isolation, which is cheaper and
  keeps the archive's documents human-readable/diffable, unlike an opaque
  DB-file copy.

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
4. **`5/16`'s owner-DB map still documents the OLD per-owner-DB
   `config_meta` counter design** (its "Owner-DB map" table lists
   `config_meta` as an owned table per DB, and its Y1-adjacent "DECIDED"
   prose describes the counter this spec now replaces with a content
   hash). Not fixed here — out of this spec's authorized scope this pass
   — but it needs a follow-up edit to `5/16` so the two specs agree; flag
   for operator sign-off before that edit ships, per this repo's
   "redesigns need sign-off" rule.

## Cross-refs

- [`17-openapi-mcp.md`](17-openapi-mcp.md) — the transport half of
  `resreg.Resource`; the REST + MCP faces this tool talks to.
- [`16-mcp-rest-unification.md`](16-mcp-rest-unification.md) — the owner-DB
  map the CLI must be repointed to. Its `config_meta` references are stale
  against this spec's content-hash CAS (open question 4, above).
- [`P-runed.md`](P-runed.md) — owns the run/spawn admission model this
  spec's filesystem-restore mechanism extends with a `kind` dispatch; that
  prose's long-term home once built.
- [`../9/2-data-model.md`](../9/2-data-model.md) — cold/warm/hot tier
  boundary; grounds why messages and secrets get different archive
  treatment than config.
- `../9/3-git-as-truth.md` — **reframed, not
  adopted** (see lead DECISION); unedited.
- `../9/4-data-ingestion-curation-eventing.md`
  — Q2/Q5 open; extend the resource set when they resolve.
- [`5-tenant-self-service.md`](5-tenant-self-service.md) — Phase C secret
  layering composes with the `secrets` resource.
- [`31-identity-pairing.md`](31-identity-pairing.md) — precedent for
  "co-locate what must be atomic" (`route_tokens.kind`), cited three times
  above as the principle this spec keeps reapplying.

## Pointers

- Engine: `resreg/engine.go`, `resreg/openapi.go`, `resreg/README.md`
- Resource declarations: `resreg/resources/*.go`
- Config CLI: `cmd/arizuko/apply.go`
- Archive CLI (unbuilt): `cmd/arizuko/archive.go`
- Run admission (unbuilt `kind` dispatch): `runed/manager.go`,
  `runed/runtime.go`, `runed/api/v1/types.go`
