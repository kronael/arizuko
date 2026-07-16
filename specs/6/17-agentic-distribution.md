---
status: reference
depends:
  [
    ../5/20-ant-portability,
    ../5/21-products,
    16-daemon-standalone-matrix,
    1-adoption-interop,
  ]
---

# specs/6/17 — agentic distribution: products as packages, arizuko as the distro

> **Recorded debate, NOT a build order (2026-07-16).** A codex critique
> (see `## Critique` below) concluded this is the `5/20` mechanism dressed
> in Debian vocabulary + an unrelated Compose axis, and should be folded
> into `5/20`, not built standalone. Kept as the reasoned exploration +
> its demolition. The useful deltas move to `5/20` under literal names; do
> NOT implement 6/17 as written. The body below is the pre-critique draft.

Can arizuko compose a running agent from packages the way a Linux
distribution composes a system? Short answer: the machinery already
exists in draft under other names. `5/20`'s product mixins ARE a
package manager (manifest + lockfile + immutable installs + blend
rules); `ant/examples/` IS a curated repository; the stock-skill
`MIGRATION_VERSION` train IS the update channel. This spec names the
mapping, closes the small gaps (search/info/remove/verify, requirement
checks, install-time scan, shareable meta-sets), and marks where the
analogy is a category error — so nobody builds the wrong half of apt.

## Problem / opportunity

Composing an agent today is either `create --product <one>` (`5/21` —
one template, all-or-nothing) or hand-work (copy skills, write
CLAUDE.md, wire env). `5/20` rewrote composition as an ordered mix of
products, but frames itself as portability, not ecosystem mechanism.
Meanwhile the field grows skill marketplaces (Claude Code plugin
marketplaces, ClawHub) with no lockfiles, no provenance, no
containment. arizuko already has the containment those lack — per-turn
ephemeral containers, default-deny egress, grants unreachable from
anything a package carries — which is what makes third-party packages
survivable at all. The opportunity is not new machinery; it is
finishing `5/20` and framing it right: capability distribution with a
distro's discipline and a sandbox no distro has.

The adoption tie (`6/1` "import, don't convert"): a product's `skills/`
dir is plain Claude Code skill format (SKILL.md + assets —
`ant/skills/tufte/` is a vendored third-party skill already). Every
existing Claude Code skill is a near-zero-cost package payload; the
format is not proprietary.

## The analogy, mapped

| Distro concept        | Debian / Nix / uv                | arizuko unit                                                                                                | Status                               |
| --------------------- | -------------------------------- | ----------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| Package               | `.deb` / derivation              | **product** — dir + `PRODUCT.md` (`5/21`); a skills-only product is the atomic package ("capability mixin") | format shipped; mixin draft          |
| Installed system      | rootfs                           | the group folder (`groups/<folder>/`) — mounted per Turn, never rebuilt                                     | shipped                              |
| Base system           | base packages + `/etc` conffiles | stock skills (~95 in `ant/skills/`) + `ant/CLAUDE.md`; `.merge-base/` 3-way merge = dpkg conffile handling  | shipped (`container/runner.go:1024`) |
| Package manager       | apt / uv                         | `arizuko product` verb family, host CLI fetches (`5/20`)                                                    | draft                                |
| Manifest              | `pyproject` deps                 | `~/products.toml` — the ordered mix                                                                         | draft (`5/20`)                       |
| Lockfile              | `uv.lock` / `flake.lock`         | `~/products.lock` — immutable rev + sha256 tree hash + migration mark                                       | draft (`5/20`)                       |
| Repository / tap      | archive / tap / AUR              | any git repo of product dirs (`git+https://…@ref#subdir`); `ant/examples/` (10 products) = bundled "main"   | draft (`5/20` sources)               |
| Version               | semver + epoch                   | the git commit + tree hash. No semver: prompts have no ABI to range over                                    | draft                                |
| Dependency resolution | SAT solver                       | **requirement check at add-time** (stock skills, env, egress, image); no transitive solve — see Risks       | partly slated, partly new            |
| Composition           | dpkg unpack + alternatives       | the per-payload-kind blend table (`5/20` §Blend rules) run by the harness seed/merge lib                    | draft                                |
| Maintainer scripts    | postinst, runs as root           | `migrations/NNN-*.md` — agent-executed under folder grants, in an ephemeral container                       | draft                                |
| Update channel        | stable/testing + DSA list        | git refs per product; for stock, `MIGRATION_VERSION` (`routd/loop.go:425`) + the migrate broadcast          | shipped for stock                    |
| Integrity             | GPG-signed Release               | lock sha256 tree hash ("the hash is the trust anchor, the URI a hint", `5/20`); signing deferred            | gap                                  |
| Malware scan          | lintian / nothing                | skill-guard pattern set (`5/23`) at install; crackbox containment at runtime                                | draft / blocked (fail-open)          |
| Distribution          | Debian itself                    | stock + curated catalog + release train; a meta-set = a shared `products.toml`                              | mostly exists                        |

## What already exists

- **`5/20` mechanism 2 is the package manager.** Ordered
  `products.toml`, `products.lock` with immutable revs + tree hashes,
  clean-replace updates, per-kind blend rules, `add/update/sync/list`
  verbs, git + file sources, rollback via `update --to <older-ref>`.
  This spec adds verbs and checks to it; it replaces nothing.
- **`5/21` is the package format.** `PRODUCT.md` (name, brand,
  `skills=` stock whitelist, `[[env]]`), optional PERSONA.md /
  CLAUDE.md overlay / `facts/` / `tasks.toml`; bundled `skills/` per
  the `5/20` delta. `cmdCreate` discovers products by scanning
  `ant/examples/` at runtime — the same scan rule a third-party repo
  needs.
- **Install plumbing is shipped.** `SetupGroup` + `seedSkills`
  (`container/runner.go:972,1024`) materialize a folder from a
  prototype; `.merge-base/` + `/migrate` own stock-file merging;
  `checkMigrationVersion` (`routd/loop.go:425`) is the single update
  trigger + broadcast (version 178 today).
- **`sync-tools-skills` is the proto-package-manager, run by hand.**
  A named-allowlist rsync of skills from an upstream tools repo, then
  version bump + image rebuild + broadcast. It proves the want;
  `product add git+…` mechanizes exactly this for non-stock payloads.

## The realization

### Package format (freeze `5/21` + two additions)

A package is a directory with `PRODUCT.md`; payload kinds are exactly
the `5/20` blend-table rows. Two additions, nothing else:

1. **`skills = […]` becomes real gating** (already slated, `5/20`
   delta): the listed stock skills are seeded for the group; unlisted
   ones are not. This is the package's `Depends:` on the base system —
   checked against the image's `ant/skills/` set at add-time.
2. **`[[egress]]` blocks** — `host`, `hint`, `required`. Today
   aws-devops carries its egress needs as PRODUCT.md comments
   (`ant/examples/aws-devops/PRODUCT.md` operator steps 3); promote
   comment to data so `add` can print exact
   `arizuko network <inst> allow <folder> <host>` commands. Reported,
   NEVER auto-granted — a package that could widen egress would breach
   the moat (`6/1` non-goals).

### Repository = source; no registry

A repository is any git repo whose subdirs carry `PRODUCT.md` — same
scan rule as `cmdCreate`. `ant/examples/` is the bundled, reviewed
"main". No central registry, no index format, no `claude-plugin:`
scheme (deferred per `5/20`). A persistent tap list is deferred until
a second real repo exists; until then `search` covers the bundled
catalog and any explicitly given source.

### Resolver + lockfile

Resolution is three passes, all at add/update time, all refuse-loud:

1. **Resolve** each source to an immutable commit + sha256 tree hash
   (`5/20`).
2. **Check requirements**: stock skills present in the image; required
   `[[env]]` keys set (missing → print `arizuko secret set` / `.env`
   skeletons, same shape as `5/20`'s apply report); `[[egress]]` hosts
   allowlisted (missing → print the `network allow` command); at most
   one `Dockerfile.ant` in the mix.
3. **Dry-run the blend** against the current folder: facts /
   `mcpServers` name collisions refuse before any file is touched;
   skill-name overrides are reported (last-in-mix wins, `5/20`).

Then write `products.lock`. `sync` installs exactly the lock. There is
no transitive resolution and no version ranges — see Risks.

### Verbs

| Verb                                   | Does                                                                                                       | Status |
| -------------------------------------- | ---------------------------------------------------------------------------------------------------------- | ------ |
| `product add <inst> <folder> <source>` | resolve → check → blend → lock                                                                             | `5/20` |
| `product update [<name>] [--to <ref>]` | re-resolve; clean-replace managed payloads; run `migrations/` above the lock mark; rollback = older `--to` | `5/20` |
| `product sync`                         | install exactly the lock, byte-identical                                                                   | `5/20` |
| `product list`                         | the mix + revs + drift flag                                                                                | `5/20` |
| `product search [<source>] [query]`    | scan bundled catalog (+ given source) for `PRODUCT.md` dirs                                                | new    |
| `product info <source\|name>`          | manifest + payload inventory by trust tier + requirement report, before installing anything                | new    |
| `product remove <name>`                | delete managed payloads (skills, mcpServers entries); seed-once payloads stay, listed in the report        | new    |
| `product verify`                       | recompute tree hashes against the lock (debsums)                                                           | new    |

`remove` has dpkg-conffile semantics and no `purge`: seeded persona /
facts / CLAUDE.md sections became the group's own state at install
(`5/20` seed/managed split); deleting them is the operator's edit, not
the package manager's.

### Composition → a running agent

Composition is **entirely at-rest file materialization**. The runtime
never sees packages: a Turn mounts the folder as always (`5/A`); routd
and runed carry zero package code. The harness seed/blend lib — the
same small Go lib `5/20` moves the 3-way merge into, successor to
`seedSkills` — is the ONE renderer that writes composed folder
contents; `create`, `add`, `update`, `sync` all call it. Every path
under the folder is owned by exactly one of {stock, package(name),
local} (`5/20` boundary rule); the lock records which. The blend table
in `5/20` §Blend rules is the composition engine; this spec adds no
rule to it.

### Trust and provenance

- **First add of a non-bundled source**: print the full payload
  inventory by tier (`5/20` extension-surface table: A seed files, B
  skills incl. scripts/binaries + mcpServers + migrations, C
  `Dockerfile.ant`), require explicit confirm. Tier C is never built
  automatically.
- **Install-time scan**: run the `5/23` skill-guard pattern set over
  every packaged SKILL.md/script at add/update, host-side in the CLI —
  a second call-site for the same scanner (the agent-hook stays for
  agent-authored skills; no parallel pattern set). Findings block;
  `--allow-findings` overrides explicitly.
- **Integrity**: the lock's tree hash; `verify` re-checks. No signing
  in v1 — signatures need key distribution, i.e. a registry; both
  deferred together.
- **Containment is the backstop**: nothing a package carries can widen
  grants, egress, or mounts, or reach outside the folder — the `5/20`
  "Never" tier; platform `settings.json` keys are rewritten every
  spawn (`seedSettings`, `container/runner.go:781`). A Debian postinst
  runs as root; an arizuko migration instruction runs as the
  folder-agent, under its grants, inside an ephemeral container.
  **Gate**: crackbox egress currently fails OPEN (BUGS, `6/16`) — the
  third-party tier does not ship before fail-closed.

### The distribution (three senses, all cheap)

1. **The base distro is arizuko itself**: stock skills +
   `ant/CLAUDE.md`, released on the `MIGRATION_VERSION` train,
   announced by the migrate broadcast. Shipped.
2. **The curated repo**: `ant/examples/`, reviewed, small. Third-party
   repos are any git URL.
3. **A meta-set is a shared `products.toml`**:
   `arizuko create <name> --products <file|url>` seeds a group with
   that mix; `--product X` stays the one-entry sugar (`5/20`). No
   metapackage `include=` recursion in v1 — sharing a file needs zero
   resolver code.

Out of scope: daemons and Go components are the other shelf (`6/16`).
Adapters install by dropping `template/services/<daemon>.toml`
(CLAUDE.md §Adding a channel adapter); components ship as imports. One
package manager does NOT unify the shelves — "`arizuko install slakd`"
is deploy-time compose, not agent composition (but see The deployment
axis below: a _product_ may bundle the daemons it needs).

## The deployment axis — a product as N containers + config

The package layer above composes an agent's **folder contents** (skills,
persona, facts). A real product is often more: N containers + a
configuration setup — a router, a DB, an adapter, a web proxy, a worker.
That topology is the _other half_ of "distribution", and it is a
different substrate. Sonnet market sweep, 2026-07-16 (sources by name):

- **Compose is the load-bearing artifact.** The Compose Spec is now
  engine-neutral (Docker/Podman/nerdctl), OCI-packageable (v5.3.0, 2026),
  and the format LLMs write and read best. arizuko already generates
  docker-compose per group/instance (`compose/compose.go`). The gap is
  not the format — it is **lifecycle**: rollback, health-gated rollout,
  drift detection, per-service log/metric surfacing.
- **"Local cloud in a box" = the self-hosted PaaS category** (Coolify,
  Dokploy, Kamal 2). This is where the 2025–26 motion is and the closest
  existing answer to "an agent stands up a customized cloud". **Coolify
  shipped a first-party MCP server (May 2026)** — an agent driving a
  running multi-container platform is now vendor-sanctioned, not a hack;
  Dokploy is the lighter-weight twin; Kamal is zero-platform-daemon
  (config is one `deploy.yml` in git, health-gated proxy handoff).
- **Orchestrators**: Kubernetes won the market (~82%) but is the wrong
  default for one 5-container product on one host. `k3s` is the honest
  answer _if_ real K8s semantics are needed; Docker Swarm is frozen;
  Nomad's ecosystem gravity is gone; ECS is AWS-only.
- **Agent-driven infra splits in two maturity tiers**: _generation_
  (intent → a correct Compose file, or Coolify MCP calls) is real and
  shipping today (Docker's own Claude Code + MCP Toolkit guide); _autonomous
  lifecycle_ (deploy/monitor/roll-back unattended) is human-approval-gated
  everywhere serious (Pulumi Neo ships "generate PR, human merges"; the
  write-capable Coolify MCP tools are community, not first-party).

**Fit — recommendation (lowest risk):** stay on Compose, add a thin
lifecycle layer; do NOT adopt a new orchestrator. arizuko's per-instance
`.env` + generated compose already rhymes with Kamal's single-file-per-
target model — borrow Kamal's health-gated proxy handoff + drift check
rather than importing a platform daemon. Wrapping Coolify/Dokploy is a
real option for a "give me a box, I run N tenants each with their own
multi-container product" story aimed at non-technical operators, but it
adds a second platform daemon + DB + release-cadence risk — a "one owner"
violation (CLAUDE.md) unless it _replaces_ compose-gen rather than living
beside it. `k3s` only when multi-host tenant placement is a real need.

**How this joins the package layer:** a product's `PRODUCT.md` may
declare the daemons/services it needs (the existing
`template/services/<daemon>.toml` + `[[proxyd_route]]` mechanism,
CLAUDE.md §Adding a channel adapter); `product add` then extends the
instance's generated compose, not just the folder contents. The blend
rule is the same "one owner per path" discipline — a product owns the
service TOMLs it ships. This is net-new and larger than the folder-
composition work; scope it as a **phase 2** of this spec, gated on the
folder-package layer landing first.

**Honest cut (sonnet):** the _generation_ half of "an agent sets up your
cloud" is real today; the _fully autonomous, no-review, in-production_
half is hype for anything past toy stacks. arizuko's bias — agent
proposes, a deterministic inspectable substrate (Compose) executes — is
exactly where the serious 2026 tooling landed. Adopt that posture; do not
promise autonomy the field hasn't shipped.

## Net-new vs reused

| Piece                                                                                                             | Status                                  |
| ----------------------------------------------------------------------------------------------------------------- | --------------------------------------- |
| Package format, manifest, lock, blend rules, add/update/sync/list, sources, seed/merge lib, migrations-in-package | **reused** — `5/20` + `5/21`, unchanged |
| Stock release train + broadcast; SetupGroup/seedSkills; `.merge-base` merge                                       | **reused** — shipped                    |
| Skill-guard scanner                                                                                               | **reused** — `5/23`, new CLI call-site  |
| `skills=` real gating                                                                                             | slated (`5/20` delta), lands here       |
| `[[egress]]` blocks + add-time requirement report                                                                 | net-new (promotes existing comments)    |
| `search` / `info` / `remove` / `verify` verbs                                                                     | net-new                                 |
| First-add trust-tier inventory confirm                                                                            | net-new                                 |
| `create --products <file\|url>`                                                                                   | net-new (thin)                          |
| Multi-container product (compose extension + lifecycle layer)                                                     | **phase 2, net-new** — deployment axis  |

## Honest risks / where the analogy breaks

1. **No ABI → no solver.** Product→product `requires` and version
   ranges are rejected: prompts expose no interface a constraint could
   check, and the mix is small (2–5) and operator-ordered.
   Dependencies here are declared requirements checked at install —
   never resolved transitively. Revisit only with a real collision
   corpus.
2. **Installable ≠ compatible.** Blend rules resolve FILE collisions;
   nothing resolves CONTEXT collisions — two mixins' CLAUDE.md
   sections can contradict, a persona's register can fight a skill's,
   with zero filename overlap. apt can prove `/usr` consistent;
   nothing proves prompts compose. Mitigation: small mixes, first-wins
   persona (one identity per group), eval turns (`10/1`) as the CI
   analog. Unsolved; the docs must say so.
3. **Supply-chain prompt injection.** A package IS text an LLM will
   obey; the regex scanner is a tripwire, bypassable by construction.
   The load-bearing defense is structural — grants/egress/mounts
   unreachable from package content — which is why the crackbox
   fail-open bug gates the whole third-party story.
4. **Seed-once payloads have no update path, by design** (`5/20`): an
   upstream persona/facts fix reaches only new installs. There is no
   "security update" for identity, and no merge base exists across
   operators' edits to add one. Document at authoring time.
5. **No maintainer corps.** Debian works because humans review the
   archive. Keep the bundled catalog small and reviewed; do not build
   a marketplace; third-party trust stays explicit per-source.
6. **Adopt the machinery, refuse the guarantee vocabulary.** The
   transport half transfers: immutable artifacts, content hashes,
   lockfile reproducibility, ordered-overlay blending, update
   channels. The guarantee half does not: dependency closure never
   implies a working agent. Use distro words for the former; never
   claim the latter.

## Acceptance

- `product add git+https://…#dir` on a fresh group installs, locks,
  and a driven turn uses a packaged skill end-to-end (`6/1` verify
  shape: spawn, drive, observe the reply).
- `product sync` from the lock alone reproduces byte-identical managed
  payloads on a second instance (hash-verified).
- Add-time report prints unset required env as `secret set` skeletons
  and `[[egress]]` hosts as exact `network allow` commands; nothing is
  auto-granted (content-level test).
- `remove` deletes only managed payloads; seeded persona/facts remain
  and are listed.
- A package with a skill-guard-tripping script is refused at add
  without `--allow-findings`.
- `create --products <file>` with two products blends per the `5/20`
  table: later skill wins wholesale, first persona wins, CLAUDE.md
  sections append in order, fact collision refuses.
- No verb writes outside the group folder + manifest/lock, and none
  touches grants, egress, mounts, or any platform table.

## Critique (codex, 2026-07-16)

Ran adversarially against this draft. **Verdict: fold into `5/20`, don't
build a standalone "distro."** The 11 findings, ranked, each with codex's
disposition:

1. **Installable ≠ composable is fatal, not residual.** Blend rules settle
   filenames; contradictory prose/personas collide invisibly. Linux
   packages compose via machine-checkable ABIs; these concatenate
   normative prose for a nondeterministic interpreter. "Small mixes + later
   evals" concedes the package-manager thesis. → cut the distro claim; keep
   deterministic file materialization as a `5/20` facility.
2. **A "product" is not one lifecycle unit.** Skills/mcpServers are
   managed; persona/facts/tasks/CLAUDE.md are seed-once — `remove` leaves
   the agent still behaving like the "removed" product; `sync` reproduces
   only part. → fold + redesign in `5/20`: split immutable seed from
   managed capability bundles.
3. **6/17 has no independent design content.** Its own inventory marks
   nearly everything "reused from 5/20+5/21"; the deltas are a few manifest
   fields + verbs — a second governing spec = ownership + vocabulary drift.
   → cut; fold deltas into `5/20`/`5/21`.
4. **The deployment axis is a different architecture stapled on.** It says
   daemons are "the other shelf", then has `product add` mutate
   instance-level Compose — crossing folder/instance ownership, no story
   for shared services / rollback / tenant scope / multi-product daemon
   ownership, and contradicting the "writes only inside the folder"
   acceptance rule. → cut; a deployment-lifecycle spec from real cases if
   ever needed.
5. **The lockfile oversells.** A tree hash proves what prose/scripts were
   fetched, not reproduced behavior (model version, stock-skill drift,
   context order, mutable seed, agent-run migrations); "trust anchor"
   authenticates nothing without a trusted expected hash/signature. →
   rename to install-manifest/receipt; claim only source-pin + drift.
6. **Most distro vocabulary imports machinery without its problem.**
   env/egress/stock-skills/image are preconditions, not dependencies — no
   interface, graph, ranges, solver, compatibility relation.
   "Resolver/Depends/repository/distribution/maintainer-scripts" are
   cargo-cult; the load-bearing bits are pin + ownership + clean-replace +
   checksum + preflight. → cut the analogy; literal names in `5/20`.
7. **The verb family is premature interface debt.** `search` finds only
   the bundled tree; `info` = an add dry-run; `verify` = the drift flag;
   `remove` is deceptive (seeded behavior remains). → cut search/info/
   verify; honest removal into `5/20` after lifecycle ownership is fixed.
8. **Containment is oversold as third-party safety.** A hostile
   skill/script/mcpServer/migration runs with the folder-agent's real
   grants, mounted tenant data, injected secrets, permitted egress —
   folder containment bounds cross-tenant blast radius, not in-tenant
   authority abuse; the scanner is bypassable and egress fails open. → cut
   the third-party ecosystem claim; operator-owned/curated only.
9. **"Agent sets up your cloud" is hype without a designed path.** The
   deployment section surveys tools but specifies no agent-facing
   contract, authz, approval, rollback; it admits serious deploys are
   human-gated → inspected Compose generation, not an agentic cloud. → cut.
10. **Runs opposite to the stated validation priority.** `6/1` says
    validation is autobiographical — get ONE external operator, not
    feature count. 6/17 proposes ecosystem UX + trust tooling + deploy
    orchestration before proving two mixed products help anyone. →
    cut/defer; validate `create --product` + the smallest `5/20` mixin
    with a real operator first.
11. **Acceptance tests mechanics, evades the thesis.** Byte-identical
    files + collision precedence don't test whether two authored packages
    stay coherent/safe/useful after composition — the one claim that would
    justify 6/17 has no gate. → require adversarial cross-product
    behavioral evals (in `5/20`) before claiming composition works.

**Response (kept for the record).** The critique lands. This draft is the
`5/20` mechanism in Debian vocabulary plus an unrelated Compose axis; two
of its OWN risk bullets (§Honest risks 2 "installable ≠ compatible", 6
"adopt the machinery, refuse the guarantee vocabulary") already said as
much — codex sharpened them into "therefore the framing is cargo-cult."
Resolution: the useful deltas (`skills=` gating, `[[egress]]` requirement
report, honest managed-removal) belong in `5/20` under literal names; the
deployment axis, if it ever ships, is its own spec from real cases; the
"agentic distribution" / Debian framing is retired. 6/17 stays as the
recorded exploration, not an implementation plan.

## Ties

`5/20` (the mechanism this finishes) · `5/21` (package format) ·
`5/23` (skill-guard, the install scanner) · `6/1` (import-don't-convert)
· `6/16` (the other shelf — daemons/components) · `5/A` (products are
recompositions; the four-layer stack) · `10/1` (eval as CI analog) ·
`compose/compose.go` (the deployment substrate) · BUGS.md (crackbox
fail-open — the third-party gate).
