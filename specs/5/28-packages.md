---
status: shipped
---

# specs/5/28 — arizuko packages

Source-first package manager: a package is a **git source** (GitHub URL,
resolved to an immutable revision) that ships a **manifest** plus any subset of
**asset kinds**. Install / upgrade / remove one mixed-asset package on an
existing instance. No registry, no OCI.

## Today's state

Skills live in `ant/skills/` and are copied into every container at spawn via
`container/runner.go:seedSkills()`; local skill edits are preserved by the
shipped `/migrate` 3-way merge (`.merge-base`, `ant/skills/self/migration.md`).
Compose adapter fragments ship as `services/<name>.yml` (`5/27`, shipped). There
is no standard way to distribute skills + fragments + routes + grants as one unit.

## The packaging cluster — who owns what

| Concern                                                                           | Spec                  |
| --------------------------------------------------------------------------------- | --------------------- |
| **Package manager** — install / upgrade / remove one unit's lifecycle             | **this spec (5/28)**  |
| Producer side — what a product/package _contains_ (`PRODUCT.md`, persona, skills) | `5/21`                |
| Composition — one group blends an ordered LIST of products (per-kind precedence)  | **this spec (below)** |
| State transport — moving a live agent's rows/files (`export`/`apply`)             | `5/8`                 |
| Compose fragment — one asset _kind_ (a file)                                      | `5/27`                |
| Prototype — a product instantiated at spawn (`5/5` Tier-3)                        | `5/5`                 |

**Rejected alternative — OCI artifacts** (spec deleted 2026-08-02). A
package as a signed, registry-hosted `ghcr.io` artifact pulled via
`oras-go/v2`, one media-typed layer per asset kind. Rejected because it buys
registry/signing infrastructure arizuko does not need while losing the thing
that makes a source-first package legible: the package IS a git repo you can
read, fork, and pin to a revision. Reopen only if build-once/ship-anywhere
becomes a real requirement (`5/5` flags prototypes as the plausible case).

**5/28 is one package's lifecycle AND how several products compose in one
group.** State transport (`export`/`apply`, moving a live agent's rows and
files) is `5/8`'s; the two meet only at restore (below).

## Package = source + manifest + asset kinds

One manifest, and it is the **shipped `PRODUCT.md`** (`5/21`) — NOT a second
`package.yaml`. A package declares any SUBSET of asset kinds (no
`skill|agent|sidecar` type enum — presence of an asset is the behaviour):

| Asset kind       | What it is                                                       | Owner handler                                                      |
| ---------------- | ---------------------------------------------------------------- | ------------------------------------------------------------------ |
| skills           | `skills/<name>/` → `<datadir>/skills/`, layered into every group | `container.SeedPackageSkills` (+ `/migrate` 3-way merge for stock) |
| compose fragment | `<name>.yml`, docker's format, included verbatim                 | `5/27` (a file)                                                    |
| config manifest  | `<name>.yaml`, a `5/8` manifest document                         | `resreg.Apply` via `arizuko apply`'s own path                      |
| image extension  | `Dockerfile.ant` (`FROM arizuko-ant` + system deps)              | operator builds explicitly, never automatic                        |

**Rows are one asset kind, not one per table.** A package used to declare
grants in `<name>-grants.json` and routes in `<name>-routes.json`, each with
its own hand-written parser and its own hot-apply loop beside the manifest
engine that already understood both — so a package could not declare a
scheduled task, a network rule or a web route at all, and its install was not
transactional (BUGS `L44`, signed off 2026-08-17: hard cutover, no dual-read).
It now ships ONE `<name>.yaml` carrying any resource `apply` writes:
`proxyd_routes`, `acl`, `scheduled_tasks`, `network_rules`, `web_routes`,
`routes`, `groups`.

Install merges those rows onto the subsystem's live projection
(`resreg.Resource.MergeRows`) and applies the whole document, because apply
rebuilds a resource from the document it is given. That is what buys the
transaction and the pre-image rollback: **a package installs whole or not at
all.** The rows it added are recorded by PK, so `remove` subtracts exactly
those (`DropRows`) and an operator's own row at another key survives.

A resource with `SkipApplyRebuild` (`secrets`, `route_tokens`, `invites`,
`onboarding`, `installed_packages`) is REFUSED at install, not accepted:
apply never writes them, so the rows would be a reported success that did
nothing — the same answer a package declaring `mcpServers` gets.

## What a package needs from `.env` — the `env` section

A package that ships a process needs values the rows cannot carry: a port, a
shared key, an upstream URL. Those live in the instance `.env`, which compose
generation reads. So the manifest carries one more reserved top-level key —
`env` — beside its resource rows. It is the same "one declaration, many
readers" rule the routes already follow: `install` provisions the keys,
`generate` resolves them into the fragment's environment, and neither invents
a second file.

An entry names exactly ONE source:

| Source           | When the value appears           | Where it lives afterwards                     |
| ---------------- | -------------------------------- | --------------------------------------------- |
| `generate: true` | minted once, at install          | written into `.env`; the operator owns it     |
| `default: <v>`   | derived on every `generate`      | never written; setting the key in `.env` wins |
| `required: true` | never — the operator must set it | `.env`, by hand, before install succeeds      |

`required` is the load-bearing one. A package whose fragment installs cleanly
and then refuses to start is the Debian failure this section exists to
prevent: the refusal belongs at install time, on the near side of the first
write, where the operator is still holding the decision. `litellm` shipped
exactly that bug — it installed, then would not start until someone wrote
`MODEL_GATEWAY_KEY` by hand.

`generate` and `default` are what "installs with working defaults" means.
After `packages add <name>`, the instance comes up on the next `generate`
with no hand-written value at all, unless the package genuinely cannot
invent one.

**`apply` refuses the `env` section.** Two readers, two parsers:
`resreg.ParseManifest` returns rows AND env needs and only `arizuko packages`
calls it; `resreg.ParseYAML` is the same decoder with the env half rejected,
and that is what `arizuko apply` calls. An operator who applies a package's
manifest by hand gets a refusal naming `packages add`, never a success that
provisioned nothing.

The `env` section is a declaration, not a script. Nothing in it runs. A
package cannot mint a capability for itself this way — the keys it names are
values, and the grants it may hold are still bounded by `--folder` at install
(§Lifecycle). An install script, if one is ever added, runs with the
OPERATOR's authority and never the package's, and it may only do what nothing
declarative can express; anything declarable stays declarative, so the apply
engine's pre-image rollback keeps covering it.

**Trust follows what's touched.** Skills, compose fragments and manifest rows
above are agent/operator space, updatable via the lock. An image
extension changes what software exists INSIDE the sandbox only — mounts,
egress, and grants stay platform-set regardless of image, so the trust
decision is supply-chain, made once, explicitly, by the operator building
it. **Never** package-modifiable: daemons, cross-folder grants, host
`connectors.toml`, platform settings keys.

**A package's authority is what the operator handed it.** Install applied
whatever a package declared until `L46` (signed off 2026-08-17), which is
supply-chain equivalence with handing over the instance while the summary read
as routine. Three bounds, all checked in `parsePkgManifests` — the one place a
package's rows enter — before the first byte is written:

- **Core route paths are reserved.** A package may not declare a path core
  arizuko serves, nor one inside a core prefix: proxyd takes the longest prefix,
  so `/dash/mine/` front-doors the operator console. The reserved set is DERIVED
  from `compose.coreProxydRoutes` (`compose.ReservedRoutePaths`), gates ignored,
  so a route added to a release is reserved by the same commit. `generate` is the
  second reader of the same manifest and applies the same rule, because
  `upgrade` rewrites a fragment without re-applying its rows.
- **Grants are bounded by `--folder`.** `arizuko packages <instance> install
  <source> [--folder <scope>]...` names the ground the package may grant within.
  `**` and `*` are refused whatever the operator named — operator-ness is
  emergent from exactly such a row — and every other scope must be a subset of
  the bound, decided by `auth.MatchGroups`, the one folder-containment matcher
  (`5/33` decision 8). No `--folder` is a bound of NOTHING, not a bound of
  everything: a package declaring any grant is refused, naming the flag it needs.
- **`owner` is stamped, never declared.** A package's `proxyd_routes` rows are
  `owner = package:<name>` (`store.RouteOwnerPackage`); a manifest that sets
  `owner` itself is refused. `arizuko` would let the row pass as core-shipped and
  `operator` would opt it out of every later generate, so an upgraded package
  could not correct its own route. `package:*` falls on the ARIZUKO side of
  `SyncProxydRoutes` — rewritten every run, keeping its attribution — because
  generate re-derives it from the package's own fragment. The operator side stays
  the only hands-off one: only an operator's `/v1/proxyd_routes` row has no
  upstream to be re-derived from.

**A package skill is delivered by `generate`, and it OVERWRITES.** The seeder
ran only at group creation, so a package's skill reached a group only if the
package was installed before that group existed, and the copy skipped files that
were already there, so no later revision could update one — while every package
verb printed "run `arizuko generate` to apply" (`L47`, signed off 2026-08-17).
`generate` now layers `<datadir>/skills/<n>/` into EVERY existing group, and the
package layer overwrites where the stock layer beneath it does not. The two trees
differ in who owns them: a stock skill is `ours` — the operator edits it and
`/migrate` 3-way merges the next release against `.merge-base` — while
`<datadir>/skills/` is written only by install and deleted only by remove, has no
merge-base entry, and the package owns the directory NAME. Two things the
overwrite does not take: a group's `.disabled` marker (dashd's per-group toggle,
the same escape hatch stock skills have) and a name a shipped skill already
holds.

A **group seed is NOT a package-install asset** — a package installs
instance-wide, but seeding a group is inherently create-a-specific-group.
`PERSONA.md`, `CLAUDE.md`, `facts/` and `[[env]]` hints are seed
content: applied once at group creation via
`arizuko create --product` / a `5/21` product (`container.SetupGroup`,
`container/runner.go:964`), then owned by the group as local state — never
re-touched by install/upgrade/remove, here or anywhere else.

`requires:` (env vars) is a preflight **warning**, never a gate.

## The installed-package record (the one new mechanism)

One mechanism, not two — the lock composition (below) needs IS this **installed
record**, one entry per installed package per install target:

- the **install target**: `(folder, name)` is the key, folder `''` meaning
  instance-wide. A package installed against the whole instance and a product
  blended into one group are the same kind of fact, so they share one lock;
- **source** + resolved **immutable revision**; local sources are stored as
  absolute paths, while Git URLs retain their identity. `catalog` names only
  packages added from the release catalog;
- the **manifest as installed** — the exact identities this install owns (route
  paths, acl keys, skill dirs, files, fragment names);
- a per-asset **content hash** (what was written).

The folder half is what makes composition's "On upstream update" column
specifiable at all: dirty-detection compares a recorded hash against what is on
disk, and for a group's mix that comparison has to name the group. Everything
`arizuko packages install` writes — compose fragments, proxyd routes, host files
— belongs to no group, so the CLI writes `''` and the sentinel is not a special
case anyone has to remember. `network_rules` already keys `(folder, target)` the
same way, `''` for a global rule.

Authorization did NOT move with the key. Both faces still bind the whole tree:
`list` reads across folders and the record names cross-folder identities (the acl
grants an install applied, every public route path it opened) whatever its own
folder says. Narrowing the read to a tenant's own subtree is composition's
decision, not the re-key's.

This is NOT per-row provenance (that was demolished — see below). It is the lock
that makes upgrade, remove, and dirty-detection specifiable at all. **Without it,
none of the three can be honest** — both codex passes (2026-07-28/29) converged
on this.

## Lifecycle

Install is a **deterministic phased plan through the owner REST handlers** —
never raw DB writes (that bypasses gate/validation/audit, `5/17`). Each asset
kind uses its OWN create/update/delete; there is no uniform overwrite verb
(proxyd `POST` rejects an existing path → update is `PATCH`; `acl` add is
`INSERT OR IGNORE`).

1. **preflight** — resolve source → revision + hash; warn on missing `requires:`;
   refuse a cross-package identity collision or an absent declared dependency.
2. **files** — skills, compose fragment, seeded files (atomic writes). A **group
   seed is written once**; thereafter it is local agent state, never re-touched.
3. **restart/health** — bring up a new sidecar; wait healthy BEFORE its route.
4. **resources** — create rows via the real handler: `proxyd_routes` by path,
   `acl` by key.

Partial failure **resumes from the installed record** (roll-forward) — fs /
compose / restart side effects can't be transactionally rolled back (`5/8`).

- **upgrade** — new revision authoritative; diff the new manifest against the
  recorded one: create/patch declared identities, delete the identities the new
  release drops.
- **sync** — `upgrade` over the whole installed-record set, in one pass. Same
  updater (`reapplyPackage`), so dirty-refusal is bit-identical; the only
  difference is disposition: `upgrade` treats a dirty asset as fatal for the one
  package named, `sync` reports it and SKIPS it so one hand-edited fragment
  cannot block the rest of the instance. Idempotent — a package whose source
  resolves to the recorded revision with the recorded hashes writes nothing and
  reports `unchanged`, so "what changed" stays truthful. This is the verb the
  blend table's "On upstream update" column names.

  **`sync` is not `relinkCatalog`.** They were conflated during design and are
  different mechanisms: `relinkCatalog` (`cmd/arizuko/main.go`, on every
  generate) converts a fragment that is still a byte-identical copy of the
  bundled catalog into a symlink, so catalog fixes reach the instance. It reads
  the filesystem, never the installed-package record, and knows nothing about
  sources, revisions, or hashes. It stays as it is.

  Only the `compose_fragment` half of a record's manifest is re-applied. The
  route, grant and skill identities are re-applied by neither verb, so
  overwriting them would strand rows `remove` is supposed to delete.

  A `catalog` record has nothing to re-fetch — its files are links the catalog
  refreshes by itself. Its `<name>.yaml` therefore moves with each release while
  the rows that manifest declared stay as `add` applied them, so both verbs
  compare the recorded asset hash against the linked manifest and REPORT that
  drift, naming the file, instead of answering "nothing to do". Installing the
  new row set is `remove` + `add`; re-applying it in place is a new contract for
  what `upgrade` means and is not built (BUGS `L315`).

- **remove** — delete exactly the recorded identities via their owner handlers
  (`DELETE /v1/proxyd_routes/<path>`, drop skills/files, bring the sidecar down in
  health→route reverse order). Refuse if another installed package declares a
  dependency. **Group seed / persona / facts are NOT removed** — they became local
  state at seed time (a seeded agent folder is not a disposable package asset).

## Local edits are protected, not overwritten

> **Correction (codex, 2026-07-29).** An earlier draft had upgrade blind-overwrite
> local edits, "safe because they were filed upstream." Data-loss regression: the
> upstream channel does not exist (`issues` only appends to a local `~/issues.md`;
> there is no `gh-issue` ant-skill or issues MCP), and local skill edits are
> preserved today by the `/migrate` 3-way merge (tested, `container_test.go:702`).

Rule: **upgrade refuses a dirty asset.** An asset is _dirty_ when its current
content hash ≠ the recorded install hash. On upgrade:

- clean asset → replace with the new revision;
- **dirty asset → stop, emit the diff** against the recorded revision; the
  operator resolves (keep-mine / take-theirs / file-the-diff to the source).

**Skills keep the shipped 3-way merge** — it already does this safely; packages
do NOT retire it. When a real upstream channel + automatic dirty-capture exist,
"file the diff" becomes one command; until then it is manual. Local is the R&D
edge, upstream the durable form (`6/`'s loop) — but nothing is overwritten unseen.

**Direction, not yet built (2026-07-14).** Today `/migrate` performs the
whole 3-way walk agent-side (`ant/skills/self/migration.md`, triggered by
`checkMigrationVersion`, `routd/loop.go:434`). The intended shape moves the
mechanical part into the harness — deterministic Go at seed/spawn time,
where `seedSkills` already walks `.merge-base` — so `new-upstream` copies,
`only-ours` keeps, and only `both-changed` becomes a conflict marker for the
agent. `/migrate` shrinks to conflict resolution only; judgment stays with
the agent, mechanics leave it. Constraint: the merge lib stays isolated from
packaging — it does not grow into a second package manager, and
`migrations/NNN-*.md` files stay agent-executed instructions by design.

## Resolves `5/27` C2

`packages remove slakd` reads the installed record, finds it owns the
`proxyd_routes` row for `/slack/`, and `DELETE /v1/proxyd_routes/<path>` through
proxyd's handler — the live table updates (not a JSON blob proxyd ignores when
its table is non-empty). The record names what to remove; no ownership guessing.

## Composition — blending an ordered product list

A group is not limited to one product: `~/products.toml` is the ordered
mix (`source =` per entry, resolved the same way packages are — local dir
or a shallow git clone pinned to its resolved revision). Two providers
share no merge base, so **blend is per PAYLOAD KIND, never a content
merge**:

| Payload                             | Blend                                                                    | On upstream update |
| ----------------------------------- | ------------------------------------------------------------------------ | ------------------ |
| `skills/<n>/`                       | → `.claude/skills/<n>/`; union by name, LAST product wins wholesale      | managed: `sync`    |
| `PERSONA.md` / `SOUL.md`            | FIRST provider wins; later warned                                        | seed-once          |
| `CLAUDE.md`                         | marked sections, in mix order                                            | seed-once          |
| `facts/`                            | union; filename collision = refuse                                       | seed-once          |
| `settings.json`                     | key union; key collision = refuse. An `mcpServers` key REFUSES the apply | managed: `sync`    |
| `Dockerfile.ant`                    | at most one in the mix                                                   | operator rebuilds  |
| `migrations/NNN-*.md`               | **refused** — nothing runs a product's migrations (below)                | n/a                |
| `tasks.toml`                        | **refused** — nothing reads a product's tasks.toml (below)               | n/a                |
| **anything else** (`PRODUCT.md`, …) | **verbatim copy; FIRST provider wins**                                   | seed-once          |

**The table is not a whitelist — the last row is what makes that true.** A
payload the table does not name is copied whole, first-provider-wins, exactly as
today's single-product seed copies it. Without that row a table-strict blend
would DROP every product's `PRODUCT.md` (10 of 10 in `ant/examples/`) and
`BRANDING.md` (2 of 10), which is a regression, not a simplification. Locked
twice: `container/product_seed_test.go` asserts every corpus file survives
`SetupGroup` byte-identical AND survives a mix of one, so neither seeder can
quietly narrow it. The blend's one deliberate departure from byte-identity is
`CLAUDE.md`, which lands inside its product's marked region (below) — its
content is still verbatim.

Two payload paths the table names but does not spell out, fixed here because a
guess would drift:

- **`settings.json` is `.claude/settings.json`** — the only settings file
  arizuko reads (`seedSettings`, `container/runner.go`). A product tree mirrors
  the group home, so that is where its payload lives and where the merged result
  lands; `seedSettings` writes arizuko's own socket entry on every spawn. A root-level
  `settings.json` is an unnamed payload and falls to the catch-all.
- **an `mcpServers` key REFUSES the apply** (BUGS `J15`, 2026-08-11). Nothing
  loads it: `ant` stopped reading it on 2026-08-09 (the settings.json MCP
  extension point was a self-grant around the HITL firewall — `5/19`, BUGS
  `J1`/`F76`), and Claude Code never read it either (its `Settings` schema has
  `enabledMcpjsonServers` for `.mcp.json`, no top-level `mcpServers`). The blend
  used to write the key anyway, so a product shipping an MCP server applied
  cleanly and delivered nothing — a silent no-op on an operator-facing path. It
  now stops the apply and names the route that works: an arizuko **connector**,
  whose tools come back over the gated socket and are therefore gated, audited
  and holdable like every other tool. Making packages REGISTER a connector
  stays open; refusing is the honest state until it is built.
- **the file's other keys union normally**, key collision refusing. Silently
  letting one provider win a key the other set is exactly the loss this row
  exists to prevent.

`SOUL.md` is the legacy `PERSONA.md` name, renamed on read
(`container/runner.go`) and only when `PERSONA.md` is absent. It therefore
shares PERSONA's row rather than getting one of its own — and a mix where one
product ships `PERSONA.md` and another ships `SOUL.md` is a **collision the
blend warns about**, because the read-time rename silently keeps the
`PERSONA.md` one and strands the other as a dead file.

### Applying a mix

`arizuko products <instance> apply <folder>` reads that group's
`products.toml`, resolves each `source` (a relative path joins `HOST_APP_DIR`,
so `ant/examples/trip` names the bundled corpus), takes each product's identity
from its `PRODUCT.md` `name`, blends, and writes ONE installed-package record
per product under `(folder, name)`. It is the only writer of a non-empty
`folder`. It is idempotent: an unchanged mix re-writes no byte and reports
`unchanged`.

An apply also DROPS what a product's last record owned and no product now
ships. "No longer shipped" is a property of the whole mix: the check is the
UNION of every current provider's hashes, so moving an asset from one provider
to another keeps it. Per-product hashes made that handoff delete the file — the
bytes were identical, so the on-disk hash still matched the old owner's record
and read as clean-and-no-longer-mine, while the apply reported `unchanged`
(BUGS `L34`). A file whose bytes moved since the record is the operator's: kept
and reported, never deleted.

`arizuko create --product` / `group add --product` seed ONE product by verbatim
copy (`container.SeedProduct`, called by `container.SetupGroup`). They are not a
degenerate mix and do not route through the blend, because
`TestProductSeedIsVerbatim` locks their output byte-for-byte and the `CLAUDE.md`
row would move it.

**Verbatim is about bytes, not about the author's layout.** Both seeders share
ONE placement rule — `container.productDest` — so a product means the same thing
however it is applied. Only `skills/<n>/` relocates, to `.claude/skills/<n>/`,
because that is the one path `seedSkills` and the agent read. The verbatim
seeder used to copy it to the group root instead, where nothing reads it, so
`--product` — the command the product catalog tells operators to run — seeded a
dead skill (BUGS `L37`). `TestBothSeedersPlaceAlike` asserts the two agree AND
that they agree on the right place.

`packages sync` covers both halves of the lock: instance-wide records go through
`reapplyPackage` as before, group-scoped ones through the mix that owns them,
once per folder — union and last-wins are properties of the whole mix, not of
one product, so a per-record re-apply would be wrong even where it did not die
looking for a compose fragment.

**Tier.** Applying a mix is COLD tier and operator-only, for the same reason
install is: it writes host files under a group home. The record it produces is
the cold-tier `installed_packages` resource, read-only on both faces
(`/v1/installed_packages` + `list_packages`/`get_package`) and gated on the whole
tree — the folder half of the key does not narrow the read, because a record
still names cross-folder identities. No agent tool applies a mix; the agent's
relationship to a product is the files in its own home.

### The marked-section convention

`CLAUDE.md` is the one payload that concatenates rather than picking a winner,
so it needs a machine-owned region marker.

**A marked-region convention already ships** — `writeManagedEnv`
(`compose/compose.go:474`) owns a block of the instance `.env` between
`# --- compose-managed (do not edit) ---` and `# --- end compose-managed ---`.
Its **semantics** are inherited here rather than re-invented, because they were
paid for: BUGS `C6` was an unbalanced marker silently dropping every operator
line after it, and the fix was to demand a single well-formed `[begin, end]`
block and fail loud on anything else.

Its **syntax** cannot be reused as-is: `#` starts a comment in `.env` but a
heading in markdown, so the same bytes would render as an H1 in a file the agent
reads as prose. Markdown's invisible comment is the HTML comment:

```
<!-- arizuko:package:<name> BEGIN -->
…product's CLAUDE.md content…
<!-- arizuko:package:<name> END -->
```

Rules — the first three are `writeManagedEnv`'s, the fourth is the extension
this payload needs:

- a product may only ever touch the bytes between its OWN markers — the region
  is the unit of ownership, and nothing else in the file is addressable;
- text outside every marked region is the operator's (or the agent's) and is
  never read, rewritten, or reordered;
- a malformed region — a BEGIN with no END, a duplicate, an END before its
  BEGIN — is a **hard error that refuses to rewrite the file**, never a
  best-effort truncate. This is `C6`'s fix restated, and it is the whole reason
  the convention is worth inheriting.
- unlike `.env`'s single block, `CLAUDE.md` carries **N regions keyed by
  product name**, emitted in mix order. Ordering among them is the mix's;
  a region's identity is its name, so re-applying one product cannot disturb
  another's.

`CLAUDE.md`'s update column is `seed-once`, so a region that already exists is
the group's state and is left byte-for-byte. That is not a reason to drop the
markers: they are why a product ADDED to the mix later gets its own region
appended without the existing ones being rewritten or reordered — the case
`.env`'s single block cannot express.

That table IS the cross-package collision rule this spec's own
install/upgrade (above) defers to when a GROUP, not an instance, is the
install target. The seed/managed split is per KIND, not per product:
skills stay upstream-managed — what `sync`
touches, same dirty-detection as any other asset above; identity
and knowledge (persona, facts) seed once and become the group's own
state, changed only by overlay (`~/CLAUDE.md`, `.disabled`, a custom
skill) or fork-and-repoint, never edited in place.

**Restore vs install:** both write rows. Rule — **restore agent state
first, then package sync reasserts package-declared identities**, so a
cloned agent's recorded packages re-install their rows over the restored
baseline.

## Reconciler alternative — demolished

> A declarative reconciler (apply/export/remove as one engine over resreg rows,
> `provenance = ownership`) was drafted here and demolished by codex
> (2026-07-28, 17 findings / 9 fatal). Chief
> kill: **provenance was a missing schema** — a `proxyd_routes` row carries
> nothing saying a package installed it. The installed-package record above is
> that schema, made minimal and explicit. Also fatal there: manifest-as-truth
> contradicts `5/8`; export≠reproduce-a-world (secrets/sessions excluded);
> "negated manifest" is meaningless. Kept as the record of why this shape is wrong.

## Deferred (to stay minimal)

Registry / marketplace / OCI / signing; semver dependency solving; fleet-wide
upgrade; shared-sidecar refcounting (declared deps + reverse-remove refusal
suffice); arbitrary agent-setup actions during install; `arizuko-package`
GitHub-topic discovery; the sidecar per-group `MCP.json` (dropped, `5/13`).

### Running a product's `migrations/` — decided: refused, not seeded (2026-08-15)

**Decided: a product may not ship `migrations/` at all.** Both seeders refuse
it and name the route that works. The runner is not built, and the payload no
longer applies-and-does-nothing while it isn't.

The mechanism the old table row named does not exist anywhere:

- the installed-package record has no mark — `Manifest` maps an asset kind to
  the identities it owns and `AssetHashes` to their content, neither of which is
  a per-product "highest migration applied";
- there is no runner. Platform migrations are agent-executed instructions fired
  by `MIGRATION_VERSION` (`routd.checkMigrationVersion` → `/migrate`), a single
  instance-wide counter with no per-product dimension. Giving products their own
  would be a second migration system beside it — the shape this spec forbids for
  the merge lib for the same reason.

Seeding the files anyway was the `mcpServers` no-op in another payload
(`J15`): the mix applied cleanly, the record listed the files as owned, and the
instructions never ran. `container/blend.go` `migrationsRefusal` now stops the
apply, and `CheckProductAssets` applies the identical rule to the verbatim
single-product seed, so neither seeder accepts what the other rejects. A product
that needs setup instructions puts them in its `CLAUDE.md` region or a skill —
both of which the agent actually reads.

Reopening needs a mark field on the record AND a decision about who runs a
product's migrations (the agent, as today, or the harness). No product in
`ant/examples/` ships `migrations/` — asserted, not assumed, by
`container/product_seed_test.go` — so the refusal costs the corpus nothing.

### Seeding a product's `tasks.toml` — decided: refused, not built (2026-08-15)

**Decided: a product may not ship `tasks.toml` at all.** `5/21` told authors it
seeded scheduled tasks for the whole life of the payload; nothing ever read the
file — no parser, no schema type, no seeder, the only mention of the filename in
any source file being the classify rule that decided where to COPY it (BUGS
`L35`). Same class as `mcpServers` (`J15`) and `migrations/` above, and the same
exit, so the rule those set holds for all three: a payload that applies cleanly
and reaches no consumer is an authoring error, not an asset. One
`refusedPayloads` map now renders every refusal, so the two seeders cannot drift
on what is refused and adding one is a row, not a second refusal path.

The alternative — build the seeding path — was live and is rejected for three
reasons, none of them "hard":

- **It is not the product's to own.** A scheduled task is a `scheduled_tasks`
  row with a real owning handler on all three faces (`schedule_task` on the
  agent socket, dashd's form, `POST /v1/tasks`). A file input would be a fourth
  writer of the same table whose only caller is the seeder — the shape this
  spec forbids elsewhere ("one renderer, many sinks").
- **The apply could not be reversed or re-applied.** `Manifest` maps an asset
  kind to the identities it owns and `AssetHashes` to file content; a seeded row
  is neither, so `products remove` could not withdraw it and an upgrade could not
  re-apply it. That is `L31`'s open question, unsigned — a payload born into it
  arrives already broken in the direction that bug names.
- **The intent already has a route that works.** A product's daily-digest task
  is real product content, and it stays expressible: the product's `CLAUDE.md`
  region tells the agent to schedule it, and the agent has the tool. That is
  the same escape hatch `migrations/` took.

No product in `ant/examples/` ships one — asserted by
`container/product_seed_test.go` — so the refusal costs the corpus nothing.
Reopening needs an ownership record for a seeded row first, not a parser.

### Composition's design gaps — all closed

Kept as the record of what was settled before the build, because each was a
sign-off-shaped decision rather than an implementation detail.

1. **The lock** (2026-08-06). It used to assert that a GROUP-home
   `~/products.toml` keys into an instance-keyed record, which it cannot; the
   exit taken was refolding the record to `(folder, name)` rather than adding a
   second, group-scoped lock — the "second package manager" this spec forbids in
   its own `/migrate` paragraph. Migration `0031` did that, `''` = instance-wide,
   and every pre-existing row is instance-wide by construction so nothing changed
   meaning. BUGS `F30` closed.
2. **The `sync` verb** — `arizuko packages sync <instance>`, `upgrade` over the
   whole record set through one shared updater. `relinkCatalog` was proposed as
   the mechanism to build it on and is **not** it: that function converts an
   identical fragment copy into a catalog symlink and never reads the
   installed-package record.
3. **The marked-section convention** — `<!-- arizuko:package:<name> BEGIN/END -->`,
   inheriting the semantics of the `writeManagedEnv` block that already ships
   (`compose/compose.go`) including BUGS `C6`'s fail-loud-on-malformed rule. The
   comment syntax differs because `#` is a markdown heading; the rules do not.
4. **The corpus mismatch** — closed by the table's catch-all row, then closed
   again from the other side. Counts at 2026-08-20 (`aws-devops` retired that
   day, BUGS `X2` option (c) — it was one of the four carrying a `CLAUDE.md`):
   `PRODUCT.md` 10/10, `SOUL.md` **6/10** (an earlier draft said 7),
   `PERSONA.md` 4/10, `BRANDING.md` 3/10, `CLAUDE.md` 3/10. Asserted by
   `container/product_seed_test.go`, so the corpus cannot drift from this
   paragraph silently.

   **`helpdesk` is the reference product** (added 2026-08-15): it ships one of
   every payload kind that has a consumer, including the first `skills/` and the
   first `.claude/settings.json` in the corpus. Adding it proved the verbatim
   lock had been passing by accident — "same path, same bytes" only held because
   nothing exercised the two payloads that transform (a skill relocates under
   `.claude/`, `settings.json` is a re-serialised key union), so the lock now
   asserts the transformed result. `tasks.toml`, `Dockerfile.ant` and
   `migrations/` remain unshipped: two of the three are now refused outright
   (`L35`), so shipping one would not apply at all.

## Code pointers

- `cmd/arizuko/packages.go` — the
  `list`/`add`/`install`/`upgrade`/`sync`/`remove` CLI; `reapplyPackage` is the
  ONE updater `upgrade` and `sync` share
- `cmd/arizuko/packages.go` `relinkCatalog` — copy→catalog-symlink, called from
  `generateCompose`; NOT the `sync` verb
- `routd/packages_store.go` (migrations `0020` + `0031`) — the installed-package
  record; `routd.InstanceWide` is the `''` folder sentinel
- `resreg/resources/installed_packages.go` — the catalog decl (RowType →
  `/openapi.json`, read-only Endpoints, agent tool names/docs)
- `routd/packages_resource.go` — the mounted handler; one renderer, two injected
  gates, both bound to the instance-wide `**` target
- `dashd/packages_page.go` — the operator's `/dash/packages/` read; `folder`
  column distinguishes an instance package from a group's product
- `cmd/arizuko/products.go` — the `products.toml` reader + `arizuko products
<instance> apply <folder>`; the ONLY writer of a non-empty record folder
- `container/blend.go` — the blend table as code (`classify` is the table's
  left column; `BlendProducts` is the rest); `CheckProductAssets` +
  `migrationsRefusal` are the refused-payload rule both seeders share
- `container/runner.go` `SetupGroup()` / `ComposeGroup()` — the two seeders,
  one verbatim tree and one blended mix, over one shared `setupGroup` skeleton
- `container/product_seed_test.go` — the verbatim-seed lock (both seeders) +
  corpus counts
- `container/runner.go` `seedSkills()` — skill seeding at spawn
- `resreg/engine.go` `ParseManifest` / `ParseYAML` / `parseEnvNeeds` — the two
  readers of one document; `EnvNeed` is the `env` entry, `PackageEnvKey` the
  reserved key
- `compose/manifest.go` `ProvisionPackageEnv` — the one resolver `packages add`
  and `generate` both call
- `cmd/arizuko/packages.go` `installPackage` — the single install path; every
  refusal lands before the first write
- `resreg/resources/proxyd_routes.go`, `routd/acl_resource.go` — the owner handlers
- `ant/skills/self/migration.md` — the 3-way merge packages reuse for skills
- `routd/loop.go:434` `checkMigrationVersion()` — the update trigger
- `cmd/arizuko/main.go:28` `productManifest` — the shared `PRODUCT.md` parse
