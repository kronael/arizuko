---
status: shipped
shipped: 2026-06-14
depends: specs/5/16-mcp-rest-unification.md, specs/5/17-openapi-mcp.md, specs/5/P-runed.md, specs/9/2-data-model.md
---

# specs/5/8 — YAML manifests + the full-instance archive

> **DECISION.** The SQLite DB is authoritative. YAML manifests are a
> transport dump/import — `pg_dump` / `pg_restore` for the cold tier — not
> a continuously-synced source of truth. No DB→YAML sync, no startup-apply,
> no SIGHUP-reload. A dump never claims to be live, so "drift" is a
> non-concept. This supersedes the git-as-truth model (a continuously-synced
> cold-tier `agents.toml`, formerly `specs/9/3`, deleted with the corpus
> minimization in `819d43cd`); committing an `export` dump to git is fine.

> **DECISION (2026-08-04, operator).** Import must transfer _everything_:
> cold-tier config, `groups/<folder>/` trees, message history, and encrypted
> secret blobs — a full instance backup/restore, not only config. That is
> why the CAS is a content hash and not a counter, why atomicity is
> per-subsystem and not cross-DB all-or-nothing, and why the archive is a
> superset artifact wrapped around the manifest.

## Status

Shipped: the resreg engine, the YAML config manifest, and the full-instance
archive, end to end.

- Config verbs reach a production instance through `openSubsystemStores`
  (`cmd/arizuko/apply.go:50`), which opens the owner DBs and errors rather
  than falling back to the frozen pre-split `messages.db`.
- Content-hash CAS: `resreg.Checksum` (`resreg/engine.go:576`), recomputed
  in-tx by `resreg.Apply` (`resreg/engine.go:609`).
- Row-level exclusion: `Resource.RowFilter` (`resreg/resreg.go:233`),
  honored by scan, delete and checksum alike.
- Archive primitives `resreg/archive.go` (`654ff3ed`), CLI
  `cmd/arizuko/archive.go` (`8aac77a8`), run-slot hold `runed/hold.go` +
  `POST /v1/holds` (`43cf6d7a`).
- `Resource.Retarget` (`resreg/engine.go:383`).

The last five gaps closed 2026-08-06, each in its own section below:

1. **The missing-group rule** — `resreg.KnownFolders` + `ValidateFolderRefs`
   (`resreg/engine.go`), run across every document by `preflightFolders`
   (`cmd/arizuko/apply.go`) before the first transaction, on both `apply` and
   `archive apply`.
2. **Cross-subsystem pre-image rollback** — `applyDocs`/`rollback`
   (`cmd/arizuko/apply.go`), with `ApplyOpts.PruneScopes`.
3. **Path-retarget wiring** — `arizuko apply --as-folder`
   (`cmd/arizuko/retarget.go`), the first non-test caller of `Retarget`.
4. **Pending onboarding admissions** — `ArchiveOnboardingRow` +
   `ExportOnboarding`/`ImportOnboarding` (`resreg/archive.go`), on the shared
   credential gate (`restoreGated`, `cmd/arizuko/archive.go`). Closes BUGS Z3.
5. **`dashd`'s kill-confirm label** — `killConfirm`
   (`dashd/runed_page.go`) reads `spawns.kind`.

Two bugs this pass found and did NOT fix, because both are redesigns:
`F41` (an empty `StampedField` is re-stamped on re-insert, so the FIRST no-op
`export | apply` moves the checksum — see "Round-trip honesty") and `F38`.
`F42` WAS fixed, because it blocked item 4 outright: `onboarding`'s row had no
`COALESCE` reads while its NULL columns are the normal state, so one pending
admission broke `Export`/`Checksum` — and therefore every config verb — for
the whole `onbod` subsystem.

## Why

`9/2`'s cold/warm/hot boundary left `agents.toml` unspecified. The config
manifest carries an instance's cold-tier config: ACL, routes, secrets
metadata, scheduled tasks, proxyd routes, web routes, network rules, group
registration. Operators additionally need to move a _whole instance_ —
config plus the data that makes it useful: what agents said, what they
remember, what credentials they hold. That is the archive.

## Two artifacts, one mechanism

The archive is a **superset that contains the config manifest**, not a
bigger manifest: `archive export` calls the same `resreg.Export`/`EmitYAML`
renderer `arizuko export` calls and writes the output as one of the
archive's documents. One renderer, two sinks.

One piece does not belong here: the `runed` admission change that lets a
filesystem restore claim a folder's run slot is general execution-plane
machinery whose long-term home is [`5/P-runed.md`](P-runed.md) — any future
folder-exclusive job (skill sync, vacuum, migration) is another consumer of
it, not another archive feature. It is specified here because the need
originated here; move the prose to `5/P`, don't duplicate it.

## Surface

Four config verbs, all in `cmd/arizuko/apply.go`:

- `arizuko export <instance> [file]` — dump cold-tier config to YAML.
- `arizuko apply <instance> <file> [--force]` — validate, rebuild.
- `arizuko get <instance> <resource>` — a scoped `export` of live state.
- `arizuko plan <instance> <file>` — non-mutating diff vs live state.

Two archive verbs, in `cmd/arizuko/archive.go`:

- `arizuko archive export <instance> [file] [--quiesced]` — config + secret
  values + message history + group filesystem trees, as one tar file.
- `arizuko archive apply <instance> <archive> [--force] [--stopped]` — full
  restore. `--stopped` asserts the instance is down; without it the
  filesystem step claims each folder's run slot in a live `runed` and
  refuses to proceed if it cannot.

**Rebuild scope is the load-bearing decision for config.** Per resource,
DELETE+INSERT is scoped to the folders the manifest mentions (`DELETE …
WHERE folder IN (<manifest scope>)`). A row's omission deletes it **only
within a mentioned scope**; groups absent from the manifest are untouched.
Instance-global resources rebuild wholesale. This is why `--prune` /
`state: absent` were cut — absence within a named scope already means
"remove". The archive's non-config subsystems (messages, secrets, group
trees) do not rebuild this way at all.

Manifest files compose additively: `apply manifest/` reads every `*.yaml`,
merges rows by PK, applies the union. **File names are informational** —
content, not name, decides what a file holds. Duplicate PKs across files
with differing payloads are a parse-time error; identical payloads
deduplicate. No `include:` directives — flat composition only, so merging is
associative and errors reproducible.

Document schema: group folder is the top-level key with owned resources
nested flat beneath it; instance-global resources (`proxyd_routes`,
`onboarding_gates`, `network_rules`) are top-level resource-kind keys with
no group wrapper. There are **no daemon section keys** — apply resolves each
resource name to its owning DB at dispatch, so a future daemon split leaves
manifests valid.

**Multi-document YAML, one `---` document per subsystem** (a subsystem = one
owner DB's config projection — `routd`, `onbod`). Exported to a directory it
is one file each; to a single path they concatenate as `---`-separated
documents; `apply` accepts either because they are the same documents. No
`--single` flag, no second format.

## Three transports, one row schema

REST, MCP, and YAML are three transports over the **same row schema**,
defined once as `resreg.Resource`. Only the **row fields** belong to the
resource; verb, identity, batching, and version are transport envelopes.
YAML's envelope is: DROP+INSERT for the verb, YAML nesting for identity,
many-rows-one-tx for batching, a content hash for CAS.

`5/17` owns the transport half (`Name`, `Endpoints`, `MCPDoc`, `Gate`,
`Handler`, `Store`). **This spec is authoritative for the row-schema half**
the engine adds — `RowType`, `Table`, `PKFields`, `Scope`, `RowFilter`,
`Hooks`, `SkipApplyRebuild` (`resreg/resreg.go:233,240`).

The archive's non-config documents (secrets-with-values, message history,
group filesystem trees) do NOT ride this mechanism — they carry data no
`resreg.Resource` addresses. Forcing them through the row-schema engine
would bloat one mechanism to serve three very different shapes: bounded
declarative config, an unbounded immutable log, and a directory tree.

## Content-hash CAS, not a counter

**DECISION (2026-08-04, operator): the CAS check is a content hash of the
canonical row projection, not a per-DB counter.** This deleted the
`config_meta` table design, `resreg.ConfigVersion`, and the "every writer
advances the version" rule this spec previously specified.

`export` stamps `checksum: sha256:<hex>`; `apply` recomputes it from the
**live DB, in the same transaction that will write**, and refuses on
mismatch unless `--force`. The hashed bytes are the SAME
`Export`/`EmitYAML` output the manifest is made of, minus the checksum field.

Why a hash, not a counter:

- It answers the actual question — "is the DB still what I exported from?" —
  directly. A counter needs **every** writer (resreg-shared and any
  direct-SQL path) to remember to bump it in the same tx; a hash needs
  nothing from writers, and catches a manual `sqlite3` edit that no counter
  would move.
- `config_meta` never existed in any split owner DB (verified 2026-08-02
  against all three live instances, BUGS.md Y1) — building it would have
  been new schema with no other consumer. A hash needs no table at all.
- It makes the `secrets`-excluded-from-the-counter carve-out unnecessary:
  `secrets` values are already outside the manifest projection
  (`SkipApplyRebuild`), so rotation never touches the hash. Nothing to carve
  out.

**Do not conflate the checksum with `ManifestDigest`**
(`cmd/arizuko/apply.go:99` — a `sha256` of the raw YAML _file_ bytes, used
only for the audit row's forensic correlation). The checksum hashes the
canonical DB-side projection; the digest hashes whatever bytes the operator
handed `apply`, edits included.

Determinism comes for free from "Round-trip honesty" (below): two
consecutive exports of an unchanged DB are byte-identical. Note the one
first-touch exception "Round-trip honesty" records (`F41`).

## Cross-subsystem apply: per-subsystem transaction, pre-image rollback

**DECISION (2026-08-04, operator, superseding this spec's prior
mixed-success / exit-3 / rerun-skip design).**

Within one subsystem there is nothing to compensate: one subsystem = one
owner DB = one SQLite `BEGIN IMMEDIATE … COMMIT` (`resreg/engine.go:609`) —
CAS recheck, scoped DELETE+INSERT, commit or rollback, native semantics, no
extra machinery.

Across subsystems (`routd`, `onbod` — the only two owner DBs with `resreg`
resources today, per `5/16`'s owner-DB map): before writing, `apply` exports
the **current** state of every subsystem it will touch into a rollback set —
the same `Export`/`EmitYAML` call the CAS hash already makes, reusing the
renderer as the pre-image producer instead of inventing a second backup
format. If a later subsystem's transaction fails after an earlier one
committed, the committed subsystem is restored by re-applying its pre-image
through the SAME `apply` codepath, fed the snapshot instead of the
operator's manifest. Shipped as `applyDocs`/`rollback`
(`cmd/arizuko/apply.go`), which `arizuko apply` and `archive apply` both call;
pre-images come from `resreg.ExportSnapshot` (one read transaction each) and
are captured for every subsystem BEFORE the first write transaction opens.

The rollback's own CAS token is the checksum the **forward apply** left
behind, not `--force`: if anything else moved that subsystem's config in
between, the rollback refuses rather than overwriting it, and the failure
names both the original error and the failed restore. A half-applied instance
must be loud.

**`ApplyOpts.PruneScopes` is what makes the rollback total**, and the earlier
design missed it: a forward apply that CREATES a folder leaves rows under a
scope the pre-image never mentions, so the pre-image's own scopes — the only
ones `Apply` would prune — do not reach them, and they survive a rollback that
is supposed to be total. The rollback therefore passes the forward manifest's
scopes as extra DELETE targets. Sound only because a pre-image is a COMPLETE
subsystem projection: pruning a scope and re-inserting the pre-image restores
that scope exactly.

One honest limit, from `F41`: "back at its pre-apply content hash" is exact
only once every server-stamped column is non-NULL. `Insert` re-stamps an empty
`StampedField`, so a `groups` row with a NULL `updated_at` comes back with the
right content but a new timestamp. Content restoration is unconditional; hash
equality waits on `F41`.

**Load-bearing constraint: the rollback restores the manifest projection,
not the database.** A whole-DB file swap would discard messages that arrived
and memory a turn wrote during the apply window — live traffic the apply
never touched. Because `Export`'s projection is exactly the config rows, and
because a subsystem's transaction is scoped-DELETE+INSERT (never a table
truncate), re-applying a pre-image touches only what the forward apply
touched. That is what makes rollback safe _here_, and exactly why it does
NOT extend to message history or filesystem trees: they are not the manifest
projection, so this mechanism has no opinion about them, and each has its
own recovery story (idempotent append; best-effort, stated as such).

This deletes the old design's premises with it: no preflight-all-owners
pass, no commit order beyond catalog order, no exit code 3, no "rerun skips
the applied owner". A failed cross-subsystem apply ends with every subsystem
back at its pre-apply content hash; a clean re-run is just `apply` again.

**Pairing edges must never be part of the projection this rollback
restores.** Found by cross-check against
[`31-identity-pairing.md`](31-identity-pairing.md): a caller `unpair`s
between the pre-image snapshot and a later subsystem's failure, and the
rollback re-inserts the edge they just revoked — silently handing back
authority they deliberately gave up, with no error and no record.
`added_by='pairing'` rows are explicit runtime consent state, made and
undone through `issue_pairing_link`/`unpair` — the same class
`SkipApplyRebuild` already carves out for secrets and tokens, not
declarative operator intent. **Rule: they belong to no manifest-visible
projection** — not the content hash, not the scoped DELETE+INSERT, not the
pre-image. Shipped as `Resource.RowFilter` (`resreg/resreg.go:233`), a
WHERE-fragment honored by scan, delete and checksum alike, set on
`acl_membership` (`resreg/resources/membership.go:79`). Role-membership rows
(`added_by` unset) round-trip normally.

## Path retargeting: primitive shipped, no caller

**Apply's data model supports "export folder X, apply as folder Y".**
`resreg.Apply` consumes already-decoded typed rows, never a file path, and
derives its DELETE+INSERT scope from the rows' OWN `Scope.Field` value
(`manifestScopes`, `resreg/engine.go:339`) — never from a reference back to
where the rows came from. So a caller can retarget rows before calling
`Apply`, with zero change to `Apply` itself.

`Resource.Retarget` (`resreg/engine.go:383`) is the engine-owned building
block, and it rewrites ONLY the declared `ScopeSpec.Field` column, for the
three resources that have one (`groups`, `web_routes`, `network_rules`).
**It refuses everything else rather than guessing**: `routes` has no
`Scope.Field` (a spawned child's route must be DERIVED from its own JID,
never copied from the parent's verbatim), `acl.scope` is a glob,
`scheduled_tasks.chat_jid` and `secrets.scope_id` are polymorphic;
`route_tokens`/`invites` are moot (`SkipApplyRebuild`, never rebuilt).

**Named gap:** a resource WITH a declared `Scope.Field` can still embed the
folder elsewhere. `web_routes.redirect_to` points into the folder's own
`/pub|priv/<folder>/` web root; `Retarget` rewrites only `folder`, so a
caller must rewrite `redirect_to` itself.

The caller is `arizuko apply --as-folder <folder>`
(`cmd/arizuko/retarget.go`) — a recipe over the two existing verbs, exactly as
this section requires, never a copy-folder mechanism of its own. Two further
consumers, named so the next agent doesn't re-derive them: `5/28`'s seed-once
package group, and cross-instance folder migration. Prototype spawn was the
third until `4a9a49c7` deleted it (with `4/26`) precisely because
copy-register-route behind one verb duplicated export→apply — see `5/5`
§"Tier 3 — Session".

Three rules the recipe carries, each inherited from `Retarget`'s own
refuses-rather-than-guesses discipline:

- **One source folder or refuse.** A manifest whose scoped rows name several
  folders is rejected; retargeting them all onto one would merge them
  silently.
- **The empty scope counts as its own folder.** `network_rules`' `folder=''`
  rows are the instance-wide egress allowlist, so retargeting them would
  narrow an instance-wide allowlist to one folder — a privilege change, not a
  rename.
- **It rewrites `redirect_to` itself**, closing the named gap above.
- **`--force` is required**, and says why: a rewritten manifest describes a
  different folder than the one it was exported from, so its checksum can
  never match the target.

**Named gap: there is no file half.** `Retarget` rewrites DB rows only, so
"export folder X, apply as folder Y" moves the rows and leaves the folder's
files behind. That is the one thing blocking a real consolidation:
`5/5` R5 (2026-08-07) chose product-only vocabulary and filed its unification
idea here — a **product** is "persona + skills + seed files copied into a new
folder", which is this section's recipe plus files, so a product could become
an exported manifest applied with `--as-folder` once a file half exists. Until
then products stay a `groups` column plus CLI-applied TOML templates
(`cmd/arizuko/products.go`) and the two shapes stay separate. Whoever builds
the file half owns the merge; do not reopen it at `5/5`.

## Consistency levels — an archive is a smear, and must say so

A full-instance archive taken from a running system is not a snapshot; it is
a smear across time, from four concrete sources:

1. **No cross-DB transaction.** `routd.db`, `onbod.db`, `auth.db`,
   `runed.db` are separate SQLite files — export reads them in sequence, so
   a write landing between two reads appears in one document and not
   another. `5/31`'s minimality pass sidestepped exactly this seam for
   pairing by putting the token in `route_tokens` so token and edge share
   one transaction (`routd/migrations/0026-route-token-kind.sql`); an
   archive spanning DBs has no single transaction to put everything in.
2. **DB vs. filesystem.** The `groups` row lives in `routd.db`;
   `groups/<folder>/` is written by `container.SetupGroup`
   (`container/runner.go:964`). An agent writing `MEMORY.md` mid-export can
   yield a config row pointing at a half-written tree.
3. **Live turns.** Messages, tool-call audit rows and memory are written
   over seconds by an in-flight turn; export mid-turn captures half a turn.
4. **`SECRETS_KEY` is deliberately absent** from the archive (below) — a
   correctness constraint, not a consistency one, but it compounds:
   ciphertext travels, the key that makes it meaningful does not.

Verified empirically against this codebase, not assumed: a single explicit
read transaction (`BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`) gives a
consistent MVCC snapshot under WAL while a concurrent connection commits new
rows — confirmed with `modernc.org/sqlite` (`store/store.go:14`) by
inserting into two tables from a second connection mid-read-tx and
confirming the read transaction's later `SELECT`s neither saw the new rows
nor changed the count already read. That is the same primitive `VACUUM INTO`
uses internally, so the row-level export path needs only the cheaper form.
`resreg.ExportSnapshot` (`resreg/archive.go:67`) wraps `Export`'s whole loop
in one such transaction; plain `resreg.Export` (`resreg/engine.go:736`)
still runs each `ScanAll` as its own implicit autocommit read.

Three levels, and the archive must **declare which one produced it** rather
than imply an image it doesn't have:

- **`live`** (default). Per-subsystem consistent — each document has its own
  read transaction — the archive as a whole not. No downtime. For cold-tier
  config this is nearly indistinguishable from a true snapshot because
  config changes are human-paced; for messages and filesystem trees the
  smear is real but bounded to the export's wall-clock duration.
- **`quiesced`** (`--quiesced`). The operator has already stopped the
  instance before running `archive export`; the flag only stamps
  `consistency: quiesced` in the metadata — the tool does not stop or start
  services itself. Trivially consistent, costs downtime, the only level that
  is honestly point-in-time. What a real DR restore wants.
- **Pre-flight validation at restore** (always, regardless of export level):
  before any subsystem's transaction opens, `apply` checks referential
  integrity across the archive's documents (the missing-group rule, below)
  and refuses the whole restore rather than importing a half-wired instance.
  Orthogonal to the smear question — it catches gross errors, not staleness.
  This is why `archive apply` BUFFERS its config documents instead of applying
  each as it streams past: a one-at-a-time pass would have committed `routd`
  before it had even read `onbod.yaml`, so neither this check nor the
  cross-subsystem rollback could exist. Config is bounded declarative config,
  so buffering it costs nothing; the unbounded document (messages) stays
  streamed.

`archive.yaml` (the archive's top-level index) records
`consistency: live|quiesced` and, per subsystem, the read transaction's
start timestamp. `archive apply` reads this and is free to refuse a `live`
archive for a use case that demands a true point-in-time image — that policy
is an operator call, not specified here; the archive just tells the truth
about what it is.

The general principle, already proven twice in this codebase: co-locate what
must be atomic (token + edge in one table, one subsystem's config in one
transaction); where you cannot, name the seam and validate at the far end
rather than pretending it isn't there.

## The full-instance archive

### Shape

**One transport shape: a single tar file — not a directory the tool also
accepts as input.** Accepting both would be two entry points into `archive
apply` (walk a directory vs. untar to a temp dir) for one artifact, buying a
convenience (`ls` without extracting) that `tar tf` already gives for free.
Contents, written by `buildArchive` (`cmd/arizuko/archive.go:248`):

```
archive.yaml             # format_version, consistency level, per-subsystem snapshot timestamps
routd.yaml               # config manifest document (this spec's existing mechanism)
onbod.yaml               # config manifest document
routd.secrets.yaml       # secret + route-token/invite VALUES — separate from routd.yaml
                         # so a plain `export` reader is never handed one
routd.messages.jl        # message history, JSONL (repo convention: .jl, not .jsonl)
groups.tar               # groups/<folder>/ trees, one entry per folder
```

`routd.yaml`/`onbod.yaml` are byte-identical to what `arizuko export` would
produce standalone — literally the same function call, written to a
different path. `groups.tar` stays a tar-inside-the-tar rather than
flattened entries because it carries filesystem trees with their own
structure and permissions — a different shape from its sibling YAML/JSONL
documents.

Not archived, and why: `auth.db` (identities, refresh tokens, signing keys)
and `runed.db` (spawn/session execution history) are outside the operator's
stated scope for a reason, not by oversight — `auth.db` is authentication
infrastructure whose signing keys rotate and whose refresh tokens shouldn't
be revived stale; `runed.db` is purely hot-tier execution history (`9/2`:
"operational state... rebuildable"), with no cold config content and nothing
an operator would want back verbatim. Restoring either onto a running
instance would be actively wrong, not merely out of scope.

### Message history

**Full, always — no time window, no opt-in flag, no `--since`.** The
operator's own framing ("a full instance backup/restore") settles this; a
partial-by-default archive would silently fail the one property asked for.
Incremental export is a different tool with a different contract (a
resumable cursor, a retention policy), not a flag away from this one.

**Format: JSONL, not YAML** (`ExportMessagesJSONL` / `ImportMessagesJSONL`,
`resreg/archive.go:136,183`). `messages` is unbounded and event-shaped; the
row-schema engine loads a resource's rows fully into a `[]RowType` and
builds a `yaml.Node` tree — a shape built for tens-to-hundreds of small
config rows, not hundreds of thousands of messages. JSONL also needs none of
the engine's CAS/diff/rollback machinery, none of which applies to an
append-only table.

**Import semantics: idempotent bulk append, not rebuild.** `INSERT OR
IGNORE` keyed on `messages.id`, so re-running the same archive, or restoring
onto a target that already has some of the same history, is a no-op on
already-present rows. It runs in batches rather than one transaction
spanning the whole history, so a multi-hour transfer doesn't hold
`routd.db`'s write lock for its duration.

**No FTS rebuild step, by construction.** `messages_fts` is
trigger-maintained (`messages_fts_ai`,
`routd/migrations/0001-initial-schema.sql:60`), and
`AFTER INSERT ON messages` fires on any ordinary SQL `INSERT` regardless of
what produced the row values. Only a raw page-level copy (`VACUUM INTO`, a
file `cp`) would skip it, and this spec already rejects that as a transport.
An `ATTACH`+`INSERT…SELECT` between two live DBs would trigger FTS equally
but loses as an _archive format_: an archive must be a durable, portable
artifact readable on a different host months later with the source DB gone,
and `ATTACH` needs both DBs mounted simultaneously and produces nothing you
can store or diff.

`turn_id`/`chat_jid` are plain `TEXT` columns carried verbatim — nothing
else in `routd.db` FK-references them.

`chats` rows (hot tier — `sticky_group`, `sticky_topic`, `is_group`) are
deliberately **not** part of this artifact: rebuildable operational state
(`9/2`), not history.

**One exception, load-bearing: `agent_cursor`.** Verified against the live
dispatch path, not assumed: `pollOnce` (`routd/loop.go:491`) reads every
message after the global min cursor, groups by chat, and enqueues a turn
unless that chat's own `agent_cursor` already covers the batch; a chat with
no `chats` row reads back `""` (`GetAgentCursor`, `routd/db.go:513`), which
sorts before every real RFC3339 timestamp. So importing history without also
setting `agent_cursor` makes the restored poller treat the entire imported
history as unseen and dispatch a turn — the agent answering every historical
message — for every restored chat. Import therefore ends by upserting each
touched chat's cursor to `MAX(timestamp)` over the rows just written
(`DeriveAgentCursors`, `resreg/archive.go:260`). This is **derived from the
imported messages, not carried from the archive** — there is no `chats.yaml`
document, and the archive ships no `chats` content beyond this one derived
column.

**Pending onboarding admissions should be archived state, not rederived from
the cursor — and are not, yet.** A route-missed message can queue a pending
admission (`routd/loop.go`'s `routeMiss` → `onbod.InsertOnboarding`) and
`routeMiss` unconditionally advances the cursor past it whether or not the
insert succeeded — a deliberate fail-forward the live system relies on (a
re-fed miss would replay forever). Setting `agent_cursor` on import
reproduces that fail-forward on a restored instance: the message is marked
seen, so it is never re-offered to `routeMiss`, so a pending admission not
independently in the archive is gone for good — the person is neither in the
queue nor going to message again. Rederiving by _not_ advancing the cursor
was considered and rejected: the archive carries no "was this a route-miss"
flag, and replaying against the target's own (possibly different) routing
rules is nondeterministic, not a reconstruction.

**This spec's earlier prescription — "register `onboarding` the same way
`onboarding_gates` is and it rides this mechanism for free" — was wrong**
(BUGS.md Z3, verified against the code): `onboarding.token` was a live
PLAINTEXT bearer and the table has no folder scope, so a naive registration
would have exported the bearer into `arizuko export` YAML AND nulled every
live setup link on any wholesale rebuild. It now IS registered, after a
hash-at-rest migration, with the bearer column omitted from `RowType`
outright and `SkipApplyRebuild: true`
(`resreg/resources/onboarding.go`) — which means it deliberately does not
ride the config lane. **That flag is load-bearing and stays**: rebuilding the
table from a `RowType` that has no `token_ref` would null every live setup
link instance-wide.

So admissions get the archive-only value-carrying document plus its own UPSERT
lane, the shape `ArchiveRouteTokenRow` already establishes:
`ArchiveOnboardingRow` + `ExportOnboarding`/`ImportOnboarding`
(`resreg/archive.go`), carried in `routd.secrets.yaml` alongside the other
value-bearing rows.

`token_ref` travels rather than being dropped because it is half of a matched
pair with `token_expires`: a row restored with a future expiry and a NULL
verifier is one `onbod` treats as having a live link that nothing can redeem.
Carrying a credential verifier is exactly why this document rides the SAME
off-by-default, proven-empty-target gate `route_tokens` and `invites` ride
("Restoring onto a populated instance"). Three documents, one gate —
`restoreGated` (`cmd/arizuko/archive.go`), because a third hand-copy of that
policy is where it would have drifted.

### Secret and token values

**DECISION (2026-08-04, operator): encrypted secret blobs travel as-is.
`SECRETS_KEY` never enters the archive. Import fails loud if the key on the
target cannot decrypt.**

The stored form is already a plain string — `"v2:" + base64(nonce ||
ciphertext)` (`store/secrets.go:21,35`) — trivially a YAML scalar, no new
encoding needed. `routd.secrets.yaml` carries the `SecretsRow` shape plus
the `Value` field the config manifest deliberately omits ("Secret safety"
below, unchanged for plain `export`).

Import is **UPSERT by `(scope_kind, scope_id, key)`, copying the column
verbatim, never DELETE+INSERT** — `secrets` keeps `SkipApplyRebuild`
(`resreg/resources/secrets.go:55`) even inside the archive path; a rebuild
would wipe live blobs. `route_tokens`/`invites` hash-at-rest values UPSERT
the same way, for the same reason "Tokens in manifests" (below) forbids
rebuilding credential material from an operator-edited manifest. Ordinary
config `apply` is unaffected; only `archive apply` touches these columns.

**`route_tokens` archives `kind='route'` only — pairing tokens are excluded
entirely** (`RowFilter`, `resreg/resources/route_tokens.go:135`). Two
independent reasons, either sufficient alone: (a) `route_tokens.owner_folder`
is `TEXT NOT NULL` today
(`routd/migrations/0001-initial-schema.sql:144`), but `5/31`'s
onboarding→pairing fold plans making it nullable for greeting-originated
links, and a `NULL` fails the generic scan into
`RouteTokensRow.OwnerFolder string` the moment that migration lands;
(b) pairing links are 10-minute single-use credentials, so archiving them has
no DR value and reviving one is actively harmful.

**The UPSERT lane can revive a revoked or consumed credential.**
`route_tokens` and `invites` revocation are both a literal `DELETE` — the row
is gone from the live table, not flagged. The bearer itself never leaks
(archives hold hashes and ciphertext), but restoring a row whose bearer link
still sits in someone's chat scrollback makes that link valid again: an
access-control regression, so "UPSERT never deletes, hence safe" answered the
wrong question. **Fix: `route_tokens`/`invites` restore is OFF by default.**
`archive apply` skips these two documents' UPSERT step and reports what it
skipped, unless `--force` — the same override the filesystem step uses, not a
third flag for a third concept — AND even with `--force` refuses unless the
target's `route_tokens`/`invites` tables are already empty (a proven-empty
target: genuine DR onto a fresh instance, not a merge or a re-run that could
clobber post-export revocations). `secrets` is exempt: it has no
revoke-by-delete lifecycle comparable to a redeemable link, and its own
per-row keyring validation is its correctness gate.

**Fails loud, validating every row, with existing code and no new exported
decrypt surface.** `store.open()` (`store/secrets.go:41`) already tries every
key in the configured keyring and errors if none authenticate — AES-GCM's tag
check is a wrong-key detector for free. One DB can hold blobs sealed under
different retired keys (a rotation mid-history), so a single probe row proves
nothing about the rest: `ValidateAndImportSecrets` (`store/secrets.go:426`)
validates every row before writing any of them, and refuses the whole secrets
step — not the whole archive; config and messages proceed independently — on
the first failure, naming the failing `(scope_kind, scope_id, key)` without
echoing the value. `store.open` stays package-private, so the CLI never gains
a decrypt capability: the exported surface is "validate and import a batch",
never "decrypt and return".

### Group filesystem trees

`groups/<folder>/` holds `PERSONA.md`, `skills/`, `.claude/` (session
state), `MEMORY.md`, `.diary/`, `media/`, prototype content — everything
`container.SetupGroup` (`container/runner.go:964`) writes when provisioning
a group, plus everything the agent writes at runtime.

**Carried: the whole tree, verbatim, as `groups.tar`. Regenerated:
nothing**, for a folder the archive actually has a tar entry for.
`SetupGroup`'s job — `mkdir`, prototype copy, skills seeding, `chownR` — is
fresh-provisioning behavior; a restored folder with real archived content is
not fresh, and re-running that scaffold over it would silently overwrite
`MEMORY.md`/`PERSONA.md`/skills with prototype defaults. `extractGroups`
(`cmd/arizuko/archive.go:658`) skips the scaffold for any folder present in
`groups.tar` and extracts the tar in its place. **`SetupGroup` stays the only
provisioning code path** (root `CLAUDE.md`: "no parallel second path") — the
"Apply is a restore" rule below is unchanged and remains the fallback for a
manifest-only apply, or for a folder the config manifest names but
`groups.tar` doesn't cover. Tar extraction is layered strictly _after_ that
rule, never instead of it.

Ordering: the config subsystem commits first (so every `groups` row exists),
then filesystem restore proceeds per folder — which is also where the
run-slot claim applies, since extraction touches a live agent's own working
directory.

### Filesystem restore claims the folder's run slot

Extracting a tar is not atomic, and a live agent can hold `groups/<folder>/`
open — writing `MEMORY.md` mid-restore is a real collision, not a
hypothetical. **DECISION: don't build a new lock. A filesystem restore runs
as a run in the folder, claiming the folder's existing spawn slot.** Shipped
as a GENERIC hold — `kind='hold'` + `POST /v1/holds` (`runed/hold.go`,
`runed/migrations/0004-spawn-kind.sql`, the same idiom `route_tokens.kind`
shipped for pairing). The restore is its first caller, not its definition.

`runed.Manager` was already a claim-or-reject executor: the admission
decision and spawn-row creation are one atomic critical section under `m.mu`
with `GetActiveSpawn(folder)` as the exclusion (`admit`,
`runed/manager.go:221`) — the exact mechanism behind "one live spawn per
folder". Claiming that slot inherits four properties instead of building
them:

- **Exclusion.** No agent turn can start while the restore holds the folder —
  the same mechanism that already stops two agent turns from overlapping.
- **Backpressure.** A message arriving mid-restore finds the slot taken and
  no steer callback registered (a containerless run registers none), so `Run`
  returns `Busy` and routd re-feeds from its own DB-backed dispatch queue —
  nothing lost, no new queueing logic.
- **Wedge protection.** `spawn` (`runed/manager.go:298`) wraps `ctx` with the
  `RunTTL` kill-deadline before calling whichever executor runs, so every
  kind gets the ceiling by honoring ordinary `ctx` cancellation instead of
  reimplementing a timer: an abandoned hold expires as `outcome=error` and
  the folder frees itself. This is what made "wedge protection already
  exists" true for a containerless kind rather than merely claimed, and it is
  why a separate lease/TTL mechanism was rejected.
- **Visibility.** It appears in `spawns`, so `dashd`'s runed page and the
  operator already see it.

Two design points worth keeping, because each was nearly decided the other
way. **Holds get their own endpoint rather than a `kind` on `POST /v1/runs`**:
`RunOutcome`'s pinned contract is "returned when the run completes", and
`Hold` (`runed/manager.go:182`) executes detached and returns a handle
immediately while `Run` (`:147`) executes inline to a turn boundary. `admit`
was EXTRACTED from `Run`, not copied — one claim-or-reject implementation,
two callers. **Release reuses `DELETE /v1/runs/{run_id}`** — a hold IS a run
and `Kill` already dispatches by kind, so it needs no route of its own. The
gate is `POST /v1/runs`' gate unchanged (`runs:run` plus folder containment;
a folder-scoped caller must not pause another tenant's folder); release
additionally needs `runs:kill`, which that route already demands.

Non-agent kinds are scoped out of four agent-only sites — the breaker update,
the reset-on-new-inbound at admission, the `MaxConcurrent` container cap (a
containerless kind consumes no container), and `session_log` bookkeeping —
and the busy-branch steer attempt skips them, otherwise a hold request would
inject an empty batch into whatever turn is live. `spawns.state`/`outcome`/
`exit_code` still record a hold's own success or failure; only the breaker's
interpretation changes.

An earlier per-folder lease/TTL design and a per-folder "pause new dispatch"
flag were both considered and rejected in favor of this. Neither is built.
`groups.open` (`store/groups.go:238`) was checked as a candidate "pause
admission" lever and rejected as a red herring: it is read _only_ by
`dashd`'s admin page for cross-group sibling visibility (spec 6/F), never
consulted by any dispatch or admission path — the `routd/db.go:228` comment
calling it "ambient turn admission" is stale prose, not evidence of behavior.

The kill-confirm reads `kind` and varies the label (`killConfirm`,
`dashd/runed_page.go`): "Stop the agent currently working for X? Any reply it
hasn't sent yet will be lost" is simply false for a hold, which has no agent
and no pending reply.

#### What else the hold serves

The reason to build this generically rather than as a restore verb: the next
folder-exclusive job costs an executor, not a design. Three are already
visible. A **vacuum** — SQLite maintenance or media/log pruning under
`groups/<folder>/` — needs the same "no turn may write here right now"
guarantee and nothing else. A **skill sync** rewriting
`groups/<folder>/skills/` (the auto-migrate path, which today races a live
agent exactly the way a restore does) is the same shape. A **per-folder
migration** rewriting on-disk state ahead of a version bump is the third.
Each either takes a hold over HTTP if it runs out of process (the restore's
path), or registers its own `kind` + executor if it runs inside `runed` — the
same admit step either way, with exclusion, backpressure, TTL and visibility
already paid for. What none of them needs is a second lock.

This is the fourth application of one principle in this spec: pairing's
token-and-edge in one transaction (`route_tokens.kind`), config rollback via
the export renderer, and now the hold onto the run slot — **don't add
coordination beside an existing serialization point, move onto it.**

#### The CLI side: `archive apply` takes the hold

`arizuko archive apply` claims each to-be-written folder's run slot between
`extractGroups`' two passes — after the extract-or-skip decision, before the
first byte lands — and releases every one when the filesystem step returns.
Only folders actually written are held; pausing a folder whose tree is left
alone would be gratuitous denial of service. The whole set is held for the
whole write because pass 2 interleaves entries across folders, so no folder
is provably finished until the pass is.

**An unreachable `runed` is FATAL.** Proceeding unguarded is the exact race
this section exists to prevent, so apply resolves the hold BEFORE opening the
archive — a restore that cannot guard the filesystem step must not half-apply
the config subsystems first — and dies naming both remedies: bring the
instance up with `RUNED_URL` reachable, or stop it and pass `--stopped`.
`--stopped` is the operator asserting no agent is running, the apply-side
counterpart of export's `--quiesced`, and the only way to skip the claim.

### The missing-group rule

**One rule: a manifest apply refuses, before writing anything, if any row —
in any subsystem document, through any reference shape (a declared FK, a
`Resource.Scope`, or a per-resource `Hooks.ValidateRow` check) — names a
folder that is not a `groups` row somewhere in the same subsystem document or
already live in the target DB.**

This closes a real gap, not a hypothetical one. Two folder references are
already real SQLite FKs and self-enforce today (`web_routes.folder`,
`route_tokens.owner_folder`, both `REFERENCES groups(folder) ON DELETE
CASCADE` — `routd/migrations/0001-initial-schema.sql:137,144`): apply's own
transaction would fail on a dangling reference for those two, noisily,
mid-write. But several folder-shaped references in this schema are
deliberately string-typed with no FK ("FK posture", below) precisely because
they aren't column-equal to a folder or are polymorphic — and SQLite catches
none of those. Today an operator's typo in one of those fields creates a
silently orphaned row instead of a validation error; a real DR restore that
got a folder name wrong would find out only much later, by absence.

The check reuses the existing scope machinery rather than inventing a
parallel validator. `resreg.KnownFolders` reads BOTH halves of "is this a
folder" — the manifests' own `groups` rows and the ones already live —
through the `groups` resource's own `ScopeSpec.Field` column, so there is one
definition and not a second. `ValidateFolderRefs` then walks every scoped
resource's `manifestScopes` (`resreg/engine.go`), the same set already used to
pick scoped-DELETE targets. `preflightFolders` (`cmd/arizuko/apply.go`) runs
it across all documents before the first transaction; `apply` refuses, `plan`
reports it. **`--force` does not override it** — force means "the DB moved
under my export", never "write rows pointing at a group that does not exist".

**The empty scope is exempt, and that exemption is load-bearing**, not a
convenience: `routd/migrations/0005` seeds two `folder=''` instance-global
`network_rules` rows into every instance, so without it a stock instance's own
`export | apply` would refuse. Proven by removing it — the pre-existing
round-trip test dies.

**Scope-declared references only; it does not guess.** The earlier draft of
this section listed `acl.principal`/`acl.scope`, `secrets.scope_id`,
`scheduled_tasks.chat_jid` and `routes.target` as a set the rule "must
validate". That was wrong on its own terms: those columns have no FK precisely
BECAUSE they are globs or polymorphic, so deciding which of them is a folder
means guessing — the exact thing `Retarget` refuses to do two sections up, for
the same columns. `Hooks.ValidateRow` (`resreg/engine.go`) remains the named
per-resource extension point for a resource that CAN decide the question for
its own column; none does today, and `secrets` cannot regress in the meantime
because `SkipApplyRebuild` means an apply never writes it.

What is actually closed, then: `network_rules.folder` — scoped, string-typed,
no FK, and the one column where a typo used to commit a silently orphaned row
(pinned by `TestMissingGroup_NetworkRulesOrphanIsRealToday`, which asserts the
orphan still lands when the preflight is bypassed). For the two FK'd
references the gain is timing, not detection: refusal before any transaction
opens instead of a noisy failure mid-write.

This does not touch or reopen "Group removal semantics" below (a manifest
_silently omitting_ a group that already exists is a different,
already-decided case) — this rule is specifically about a reference to a
folder that was never a group at all.

### Restoring onto a populated instance

Config apply was never dangerous here — CAS-checked, scoped, blast radius
exactly the rows the manifest mentions. The archive's non-config subsystems
diverge:

- **Secrets:** UPSERT, always on. A secret has no revoke-by-delete lifecycle
  a revival could reactivate; per-row keyring validation is its gate.
- **`route_tokens`(`kind='route'`)/`invites`: revival risk, gated off by
  default.** Skipped unless `--force`, and even then refused unless the
  target's table is already empty. Pairing tokens are never archived at all.
- **Message history:** safe by construction — `INSERT OR IGNORE` never
  overwrites or deletes an existing row. Restoring an archive twice, or onto
  an instance that already has some of the same history, converges to the
  union.
- **Filesystem trees: the actual danger.** Extracting `groups.tar` onto a
  folder that already has a non-empty tree overwrites live content — whatever
  the current agent has written since the archive was taken. **Rule: `archive
apply` refuses a folder's filesystem step (skips it, reports it; that
  folder's config and messages steps proceed regardless) when the target tree
  is non-empty, unless `--force`** — the same flag `apply --force` already
  means "override a built-in safety refusal", not a second flag for a second
  concept. A genuinely fresh instance (the DR case) needs no flag, because
  there is nothing to clobber; a populated target (re-running an old archive,
  merging instances) requires the operator to say so explicitly. The
  per-folder run-slot claim applies regardless of `--force` — not corrupting
  a tar mid-extraction while a turn is live is a different hazard from
  overwriting the tree's prior contents on purpose.

### Cross-instance portability

**JIDs and folder paths travel verbatim — nothing is rewritten.**
`messages.chat_jid`, `route_tokens.jid`, `scheduled_tasks.chat_jid` stay
bound to the _source_ instance's channel credentials (a specific WhatsApp
number, a specific Telegram bot token); a restored chat won't resume routing
until the same external account is reconnected on the target. No FK enforces
these, so nothing breaks on import — it's an operational fact, not a data
bug, and there is no correct rewrite target without operator input. Same
reasoning covers `WEB_HOST` (never embedded in a row — read from the
environment at call sites, so there is nothing to rewrite) and already-issued
token/pairing URLs (the `WEB_HOST` a recipient already has is baked in at
mint time, not stored, so the URL breaks on restore even though the token row
itself imports fine). `SECRETS_KEY` never travels, for the same class of
reason: cross-instance secrets restore needs the operator to carry the key
out-of-band.

**Refuse an older target binary; don't try to translate for it.**
`archive.yaml` stamps `format_version: N` — a small integer bumped only when
an archive document's shape changes, independent of `arizuko`'s release tags.
`archive apply` refuses outright when it exceeds the target binary's
compiled-in `resreg.ArchiveFormatVersion` (`resreg/archive.go:25`): failing
loud beats silently ignoring fields an older binary doesn't know about.

## Apply is a restore, so filesystem prep follows the commit

Group filesystem state (skills, `.claude/`, prototype) is **eventually
consistent with the DB** — filesystem ops cannot join a SQLite tx. After the
config tx commits, `apply` calls `container.SetupGroup(folder)` for every
group row lacking a complete on-disk dir. A failed `SetupGroup` surfaces as
an apply error, never swallowed: a row without its dir makes routing `docker
run` against a missing path and exit 125. `arizuko repair` re-runs the prep
alone, idempotently. Direct `mkdir` of a group is forbidden (CLAUDE.md). For
`archive apply` this rule is unchanged and stays the fallback; a folder with
a `groups.tar` entry skips the scaffold and extracts the tar instead.

Removing a group from the manifest deletes its row but **not** its directory;
`arizuko group purge <folder>` does full removal.

## Group removal semantics

**DECISION: when apply removes a `groups` row, active routing state is
cleared in the same tx; runtime history is not.**

Cleared in the DELETE tx — live refs that would silently misroute if left
dangling: `chats.sticky_group`, `chat_reply_state.engaged_folder`,
`group_watchers` (either side), `router_state` cached pointers.

Left intact, keeping the orphaned folder string for forensics: `messages`,
`audit_log`, `cost_log`, `secret_use_log`, `task_run_logs`.

Full erasure is `arizuko group purge <folder>` — intentionally imperative and
destructive in a way YAML apply is not. `plan` warns on group removal, naming
the routing rows cleared and the history rows stranded.

## Tokens in manifests

**DECISION: a resource whose PK is a system-generated secret must never be
rebuilt from a manifest.** A full rebuild would wipe live tokens; preserving
them would need either secret values in YAML or a name-indirection layer —
both disproportionate. Tokens stay imperative (`arizuko invite …`, `arizuko
token …`); their mutations still write an audit row. Under the content-hash
CAS they need no counter bump either — the hash is recomputed fresh from live
rows, so a token mutation between two config applies is simply reflected the
next time either side reads the DB.

Enforced by `SkipApplyRebuild` on `secrets`
(`resreg/resources/secrets.go:55`), `route_tokens`
(`resreg/resources/route_tokens.go:128`), `invites`
(`resreg/resources/invites.go:60`) and `onboarding`
(`resreg/resources/onboarding.go:95`).

`archive apply` does not reopen this: its secrets/tokens UPSERT lane is a
_different_ codepath from config `apply`'s DELETE+INSERT, added specifically
because it never rebuilds — it only ever adds or matches an existing row by
PK. Ordinary `arizuko apply` on a hand-edited manifest still never touches
these columns.

Deferred: v2 encrypted token export (operator-supplied key, `token:
'enc:AES-GCM:<b64>'`) for the _config_ manifest specifically — superseded in
spirit by the archive's as-is UPSERT lane, which covers this need for the
full-backup case; the config-manifest-only path stays metadata-only.

## Config vs runtime tables

Two table classes by **documentation discipline** — no prefixes, no separate
files, just a rule about which class owns which table. Config tables are
operator-authored cold-tier intent; runtime tables are system-generated
record. The canonical membership list is the set of registered resources
(`resreg/resources/*.go`), not a table here.

Rules that must be upheld:

1. `apply` writes only config tables. **One named exception:** removing a
   `groups` row clears that group's routing side-channels in the same tx.
2. Runtime tables are never bulk-DELETEd — only by explicit retention/purge.
   The archive's message import is additive-only (`INSERT OR IGNORE`), so it
   doesn't violate this; it was never a DELETE+INSERT consumer of the engine.
3. Cross-class JOINs are expected (dashd, reporting). The split is a
   write-discipline boundary, not a query boundary.
4. No new table joins the config class without a `resreg.Resource`. A table
   that is not manifest-addressable belongs to the runtime class.
5. **No daemon may cache config-table rows in memory** (normative). One
   indexed read is cheaper than any cache invalidation, and an in-memory
   config cache creates a stale-read window that makes apply semantics
   undefined. The one allowed cache is
   `sync.Map[backendURL]*httputil.ReverseProxy` — it caches connections, not
   config rows; the row that picked the URL is re-read per request.
   (Shipped: proxyd's `routesResource` mutex+snapshot is gone —
   `proxyd/resource.go:5`.)

Restore semantics: all daemons see new config on their next DB read — no
signals, no reload endpoints, no cache invalidation. WAL gives readers
snapshot isolation during the tx.

## Round-trip honesty

`get`/`export` must emit a fragment that re-applies to a no-op — exact shape
`apply` accepts, no extra or omitted fields, no reordering.

**Canonical key order is mandatory** (Go map iteration is
non-deterministic): group folders lexicographically, then global resource
keys lexicographically; within a group, catalog order; within a list, rows
sorted by PK. Two consecutive exports must be byte-identical on an unchanged
DB or file hashing and git diffs break — and the content-hash CAS above
depends on exactly this guarantee holding.

**One known first-touch violation, `F41`.** `Insert` fills any `StampedFields`
entry that is still empty with `now()`, and `groups.updated_at` is nullable
with no default — so a row holding NULL there exports as `updated_at: ""`, and
re-applying that unmodified manifest writes a fresh timestamp. The checksum
moves once, then is stable. Both candidate fixes change a settled meaning
(drop stamped fields from the hashed projection, or stop reading an exported
`""` as "unset"), so it is recorded for sign-off rather than patched here.

## Secret safety

Secret blobs never appear in the **config manifest's** YAML (metadata only),
`plan` output (shown as set/unset), error payloads, or audit rows. `secrets`
carries `SkipApplyRebuild` (`resreg/resources/secrets.go:55`) so config
`apply` validates and diffs the metadata but never DELETE+INSERTs — a rebuild
would wipe the imperatively-set blob. Setting one outside the archive path is
a separate operator command, `arizuko secret set`. Trust boundary unchanged
from [`9/2` "Entity notes worth keeping"](../9/2-data-model.md#entity-notes-worth-keeping).

This guarantee is specifically about `arizuko export`/`apply`/`get`/`plan` —
the config manifest. `archive export`/`apply` is a **different,
narrower-audience artifact** (`routd.secrets.yaml`) that exists precisely to
carry the value, under the as-is/UPSERT rule decided for it. An operator who
only ever runs the config verbs never produces or consumes a file containing
a secret value; only `archive` does, and it says so in its own filename.

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
which is exactly why it gets its own JSONL lane instead of being forced into
one side of this rule.

## Status is not in the manifest

A dump carries cold-tier config rows only; live state is read by `arizuko
get`. Dumps never carry `status:` / `applied_at:` / `last_error:` — the same
spec/status boundary `kubectl` draws. `archive.yaml` is the one exception by
design: it exists specifically to record the archive's own consistency level
and snapshot timestamps, because an archive that doesn't say how live it is
cannot be restored responsibly.

## FK posture

**FKs are ON globally** — the pragma rides the DSN, because
`modernc.org/sqlite` defaults them off per connection (`store/store.go:96`).
Three FKs are declared and self-enforce, all CASCADE: `task_run_logs →
scheduled_tasks` (`routd/migrations/0009-tasks.sql:25`), `web_routes.folder →
groups` and `route_tokens.owner_folder → groups`
(`routd/migrations/0001-initial-schema.sql:137,144`).

Everything else cross-table is **intentionally string-typed — no FK**, so
SQLite catches none of it. One of those is a folder by declaration and IS
validated by "The missing-group rule": `network_rules.folder` (whose `''`
instance-global rows are why a FK would reject it). The rest are string-typed
because they are globs or polymorphic — `acl.principal`/`acl.scope`/
`acl_membership`, `secrets.scope_id` (polymorphic by `scope_kind`),
`scheduled_tasks.chat_jid`, `routes.target` (not column-equal to a folder) —
and the rule deliberately does NOT guess at them; see that section for why,
and for the per-resource escape hatch if one ever becomes decidable.
`messages`/`audit_log`/`cost_log`
reference folders too but are left dangling on group delete, deliberately,
for forensics — see "Group removal semantics" for the routing-state tables
that DO get cleared instead.

Implementation constraint: standard Go idioms only — `reflect`, struct tags,
`database/sql`, `gopkg.in/yaml.v3`, `encoding/json`. No DSLs, no codegen, no
third-party ORMs.

## Cross-refs

- [`17-openapi-mcp.md`](17-openapi-mcp.md) — the transport half of
  `resreg.Resource` (REST + MCP faces this tool talks to), and sole owner of
  OpenAPI emission.
- [`16-mcp-rest-unification.md`](16-mcp-rest-unification.md) — the owner-DB
  map. Its `config_meta` references are stale against this spec's
  content-hash CAS — needs a follow-up edit so the two specs agree; flag for
  operator sign-off before that edit ships, per root `CLAUDE.md`'s "redesigns
  need sign-off" rule.
- [`P-runed.md`](P-runed.md) — owns the run/spawn admission model the
  filesystem-restore hold extends with a `kind` dispatch; that prose's
  long-term home.
- [`../9/2-data-model.md`](../9/2-data-model.md) — cold/warm/hot tier
  boundary; grounds why messages and secrets get different archive treatment
  than config.
- [`5-worlds-agents-sessions.md`](5-worlds-agents-sessions.md) — Phase C
  secret layering composes with the `secrets` resource.
- [`31-identity-pairing.md`](31-identity-pairing.md) — precedent for
  "co-locate what must be atomic" (`route_tokens.kind`); source of the
  `added_by='pairing'` exclusion and the `kind='route'`-only archival rule
  above, plus one forward risk (`owner_folder` going nullable per the
  onboarding→pairing fold, `709ea647`).

## Pointers

- Engine: `resreg/engine.go`, `resreg/openapi.go`, `resreg/README.md`
- Resource declarations: `resreg/resources/*.go`
- Archive primitives: `resreg/archive.go`, `store/secrets.go`
  (`ExportSecretRows`/`ValidateAndImportSecrets`)
- Config CLI: `cmd/arizuko/apply.go`
- Archive CLI: `cmd/arizuko/archive.go`
- Run admission + hold: `runed/manager.go`, `runed/hold.go`,
  `runed/api/v1/types.go`
