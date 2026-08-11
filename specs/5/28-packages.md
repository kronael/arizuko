---
status: defected
defects: [C2, J15]
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

| Asset kind       | What it is                                                    | Owner handler                                     |
| ---------------- | ------------------------------------------------------------- | ------------------------------------------------- |
| skills           | `skills/<name>/` → `<datadir>/skills/`, layered by seedSkills | `seedSkills` (+ `/migrate` 3-way merge for stock) |
| compose fragment | `<name>.yml` (+ `<name>-routes.json`)                         | `5/27` (a file)                                   |
| proxyd route     | a `proxyd_routes` row, keyed by path                          | `store.PutProxydRoute` (live)                     |
| grant            | an `acl` row, keyed by identity                               | `store.PutACLRow`                                 |
| image extension  | `Dockerfile.ant` (`FROM arizuko-ant` + system deps)           | operator builds explicitly, never automatic       |

**Trust follows what's touched.** Skills, compose fragments, routes, and
grants above are agent/operator space, updatable via the lock. An image
extension changes what software exists INSIDE the sandbox only — mounts,
egress, and grants stay platform-set regardless of image, so the trust
decision is supply-chain, made once, explicitly, by the operator building
it. **Never** package-modifiable: daemons, cross-folder grants, host
`connectors.toml`, platform settings keys.

A **group seed is NOT a package-install asset** — a package installs
instance-wide, but seeding a group is inherently create-a-specific-group.
`PERSONA.md`, `CLAUDE.md`, `facts/`, `tasks.toml` and `[[env]]` hints are seed
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
- **source** + resolved **immutable revision**;
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

| Payload                             | Blend                                                                    | On upstream update       |
| ----------------------------------- | ------------------------------------------------------------------------ | ------------------------ |
| `skills/`                           | union by name; LAST product wins wholesale                               | managed: `sync`          |
| `PERSONA.md` / `SOUL.md`            | FIRST provider wins; later warned                                        | seed-once                |
| `CLAUDE.md`                         | marked sections, in mix order                                            | seed-once                |
| `facts/`, `tasks.toml`              | union; filename collision = refuse                                       | seed-once                |
| `settings.json`                     | key union; key collision = refuse. An `mcpServers` key REFUSES the apply | managed: `sync`          |
| `Dockerfile.ant`                    | at most one in the mix                                                   | operator rebuilds        |
| `migrations/NNN-*.md`               | union; filename collision = refuse                                       | seed-once (see Deferred) |
| **anything else** (`PRODUCT.md`, …) | **verbatim copy; FIRST provider wins**                                   | seed-once                |

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

`arizuko create --product` / `group add --product` are unchanged — one product,
seeded by verbatim copy (`container.SetupGroup`). They are not a degenerate mix
and do not route through the blend, because `TestProductSeedIsVerbatim` locks
their output byte-for-byte and the `CLAUDE.md` row would move it.

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

### Running a product's `migrations/` — deferred, and why

The blend SEEDS `migrations/NNN-*.md` (union, filename collision refuses, so no
provider's file is lost). Nothing RUNS them, and the table's old "run above the
lock's mark" is retired rather than implemented, because it names a mechanism
that does not exist anywhere:

- the installed-package record has no mark — `Manifest` maps an asset kind to
  the identities it owns and `AssetHashes` to their content, neither of which is
  a per-product "highest migration applied";
- there is no runner. Platform migrations are agent-executed instructions fired
  by `MIGRATION_VERSION` (`routd.checkMigrationVersion` → `/migrate`), a single
  instance-wide counter with no per-product dimension. Giving products their own
  would be a second migration system beside it — the shape this spec forbids for
  the merge lib for the same reason.

Building it needs both a mark field on the record and a decision about who runs
a product's migrations (the agent, as today, or the harness). That is a design
call, not a build, so the row ships as seed-once and this stays deferred. No
product in `ant/examples/` ships `migrations/` — asserted, not assumed, by
`container/product_seed_test.go`.

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
4. **The corpus mismatch** — closed by the table's catch-all row. Verified
   against the tree rather than recalled — `PRODUCT.md` 10/10, `SOUL.md`
   **6/10** (an earlier draft said 7), `PERSONA.md` 3/10, `BRANDING.md` 2/10,
   `CLAUDE.md` 3/10, and still no product ships `skills/`, `tasks.toml`,
   `settings.json`, `Dockerfile.ant` or `migrations/`. Those counts are asserted
   by `container/product_seed_test.go`, so the corpus cannot drift away from this
   paragraph silently.

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
  left column; `BlendProducts` is the rest)
- `container/runner.go` `SetupGroup()` / `ComposeGroup()` — the two seeders,
  one verbatim tree and one blended mix, over one shared `setupGroup` skeleton
- `container/product_seed_test.go` — the verbatim-seed lock (both seeders) +
  corpus counts
- `container/runner.go` `seedSkills()` — skill seeding at spawn
- `resreg/resources/proxyd_routes.go`, `routd/acl_resource.go` — the owner handlers
- `ant/skills/self/migration.md` — the 3-way merge packages reuse for skills
- `routd/loop.go:434` `checkMigrationVersion()` — the update trigger
- `cmd/arizuko/main.go:28` `productManifest` — the shared `PRODUCT.md` parse
