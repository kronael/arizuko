---
status: draft
depends:
  [
    4/9-acl-unified,
    3/5-tool-authorization,
    4/19-action-grants,
    5/A-primitives-framing,
  ]
supersedes-in-part: [3/5-tool-authorization, 4/19-action-grants]
---

# specs/4/R — paths, roles, and grant-option (remove tiers)

> arizuko stops deriving capability from location. A **path** is where you are;
> a **role** is what you may do; a **grant** carries a `WITH GRANT OPTION` level
> that governs re-delegation. Authority flows by delegation (a subset of what you
> hold), never by a depth number. Postgres's delegation simplicity over the AWS-
> shaped policy row `4/9` already ships.

## Why

Today capability leaks from coordinate (`auth/identity.go:23` `Resolve`:
`tier = min(count("/"), 3)`), and default power is computed from that depth
(`grants/grants.go:176` `DeriveRules`), and the authz boundary is a folder-prefix
walk (`auth/policy.go` `AuthorizeStructural`). You cannot give a deep path broad
power or a shallow path narrow power without fighting the tree, and delegation is
gated by an arbitrary number. We remove the tier axis **entirely**.

The role machinery to replace it **already exists** — shipped at the v0.38.0 ACL
cutover (migration `0053`): `acl(principal, action, scope, params, predicate,
effect)`, `acl_membership` (role membership + role→role hierarchy), `role:`
principals, transitive `Ancestors()` with cycle prevention, deny-wins evaluation
(`auth/authorize.go`). The `folder:<path>` principal is the path↔role bridge
(`4/9` Open-Q3, "Lean: yes", now decided here). Net-new is small.

## The three primitives

**path** — a location/coordinate: the JID prefix, container home, web vhost,
routing target, the tree you navigate. A string (`acme/eng`). Carries **no**
permission. (Today's "folder"/"group", renamed — see §Rename.)

**role** — a named capability bundle (Postgres `ROLE`): a set of grants,
addressable as `role:<name>`, **composable** via `acl_membership` (role→role =
inheritance). Already shipped.

**grant** — one AWS-shaped permission row `(action, scope, params, predicate,
effect)` — already shipped — **plus** a new **grant-option** level:
`read | write | grant`. `grant` = the holder may re-delegate this permission (or
a subset) onward. Postgres `WITH GRANT OPTION`.

Permission = expand principal's roles transitively → union of grants → match
`(action, scope, params, predicate)` → deny-wins. **Nothing is derived from path
depth.**

## Resolved model (operator decisions, 2026-07-30)

These close the open design questions; the rest of the spec is read through them.

1. **No root group. No tier-0-by-location. Root is a GRANT.** The operator holds a
   root grant and invokes it by the `/root` command; power comes from holding the
   grant, never from sitting at an empty/top folder. Remove the "tier 0 = root by
   location" model and the ~10 `id.Tier == 0` location checks — each becomes "does
   the caller HOLD the root grant (and, for a turn, invoke `/root`)". There is no
   root folder to occupy.

2. **Tier is completely dissolved — it survives ONLY as the path hierarchy.** The
   path (`world/org/team`) is a pure coordinate: routing target, JID prefix,
   container home, web vhost. It carries **zero** authorization. Depth means
   nothing to authz.

3. **Grants flow by lineage/delegation, not location.** A new group is granted its
   capabilities by its **creating group, or a higher group in its lineage** — via
   the subset-of-held `grant_option` rules (a granter may only pass what it holds
   WITH the option). Each new group starts with the grants its creator delegates;
   no default falls out of where it sits. Escalation = asking a lineage ancestor
   (or the operator's root grant) for more.

4. **Lineage is canonical; the filesystem path is a projection.** A group is best
   understood by _what created it_ (its creation lineage), not by its folder
   location — the path is a convenient rendering of that lineage, not the source of
   truth. Consequence: groups **know about each other** and can **send files and
   messages to one another** (cross-group `send`/`send_file`), gated by grants, not
   by tree position.

5. **Only worlds + delegated subgroups.** Top-level = a **world** (where users
   onboard). Everything under it is a subgroup that exists via the delegation +
   escalation rules above, bound to its lineage. No third structural tier, no root
   group.

6. **Mounts and skill-modification are grants.** Filesystem access is a grant with
   a read-only vs read-write level (mount RO / mount RW). Skill modification is a
   grant too — **EXCEPTION: global skills are operator-only.** A group can no longer
   modify a global skill directly (a change from today); it may only **request** the
   operator modify it (via the issue/skill-request path). Per-group/custom skills a
   group with the grant may modify; global skills are the operator's alone.

7. **One default role, mostly unprivileged.** The old `role:tier0..3` (four depth
   bundles) collapse to a SINGLE seeded default — `role:unpriv` — with little more
   than the floor (reply/send in its own thread; essentially no management verbs).
   Every group is born with it. Everything beyond the floor is **explicit
   delegation** from the creating group or a lineage ancestor (decision 3). No
   `open`/`closed` split, no per-depth bundle. (`role:operator`/root stays as the
   escalation grant per decision 1: the operator, holding root, seeds a world's
   first real grants at create; those cascade down by delegation.) This is the
   minimal-default / everything-explicit model — a group has exactly the power its
   lineage handed it, nothing implied by where it sits.

## Delegation replaces tiers

There are no tiers. The capability boundary is emergent and Postgres-clean:

> **You may grant onward only a SUBSET of the grants you HOLD, and only those on
> which you hold the `grant` option.**

Canonical case — an agent spawns a sub-agent at a child path (or an admin
onboards a user):

1. The spawner writes, for the child's `folder:<childpath>` principal (or a
   `role:` it mints), `acl` rows that are a subset of the spawner's own held
   grants.
2. Each written row must be **covered by a held row carrying `grant_option`**
   (the subset-delegation check — §Check). A grant re-delegated _without_ the
   option lands the child at `write` (usable, not re-grantable): the chain
   narrows monotonically.
3. Authority strictly decreases down any delegation chain **without a depth
   number**. A "top" principal just holds broad grants with the option; a leaf
   holds a narrow subset with none. Path depth is irrelevant.

This retires `delegate_depth`/`tier` claims (`4/9:221`) as authz inputs.

## Grant-option: schema

One column on `acl` (the minimal move — the action lattice `* ⊃ admin ⊃ interact`
stays for _coverage_, orthogonal to _delegability_):

```sql
ALTER TABLE acl ADD COLUMN grant_option INTEGER NOT NULL DEFAULT 0; -- 0=no, 1=WITH GRANT OPTION
```

`read`/`write` is already expressed by the `action` (`mcp:inspect_*` vs
`mcp:send`, or `interact` vs `admin`); `grant_option` is the third, delegation
axis. A row with `grant_option=1` means "the holder may re-grant this
(action,scope,params) or a subset of it."

## Subset-delegation check

`Delegate(granter_principal, target_principal, rows []acl) error` — used at spawn
and at any `add_acl` where the writer is not `role:operator`:

```
for each row r in rows:
    held := ACL rows of granter's expanded principal set with grant_option=1
    require ∃ h in held such that:
        actionCovers(h.action, r.action)  AND
        globCovers(h.scope, r.scope)       AND   -- h.scope ⊇ r.scope
        paramsCover(h.params, r.params)    AND
        r.grant_option ≤ h.grant_option          -- can't mint an option you lack
    else → deny "cannot delegate <r>: not held WITH GRANT OPTION"
```

`role:operator` (holding `(*, **, allow, grant_option=1)`) delegates anything —
the sole seeded root of every chain.

## Default roles (solo stays zero-config — 5/A)

Removing tiers must not make a solo user configure roles. Seeded at
`arizuko create` / path-create:

- `role:operator` — `(*, **, allow, grant_option=1)`. First OAuth sub bound at
  create. (Already seeded.)
- `role:owner` — the grant bundle old **tier 1** gave (platform actions +
  `register_path`/`delegate_path` + own-subtree `admin`), **with** grant_option
  on its own subtree. Minted for a path's creator at create.
- `role:member`, `role:reader`, `role:guest` — bundles for old tier 2 / 3+ /
  guest, **without** grant_option. Assigned by invite/onboard.

Path-create mints `acl_membership(creator_sub → role:owner)` scoped to the new
path — a **one-shot seed at create**, NOT a live parent-walk. That is the line
between convenience and the coupling we deleted (§Open-Q on re-coupling): a child
does not _inherit live_ from its parent path; it is _granted once_ at birth and
diverges freely thereafter.

## What changes in code (the coupling trio dies)

**The load-bearing change** (found in the adversarial pass): today the agent is
NOT an `acl` principal — the spawn injects `in.Grants []string` = the
`DeriveRules(folder, tier, world)` output (`routd/dispatch.go:539`,
`routd/mcp.go:510`) plus `ARIZUKO_TIER` env. To decouple, **the agent becomes a
first-class `acl` principal**: `folder:<path>` with `acl` rows seeded at
path-create (from its default role) + delegated at spawn. Spawn resolves the
child's grants from `acl` (its expanded principal set), NOT from `DeriveRules`.
This single change is where containment, solo-config, and the mcp:\* toolset all
converge — do it first.

- **DELETE** `auth/identity.go:23` `Resolve` tier derivation; keep `WorldOf` only
  as a routing/vhost coordinate helper (not authz). Remove `tier`/`delegate_depth`
  agent claims + `ARIZUKO_TIER`.
- **DELETE** `grants/grants.go:176` `DeriveRules` (tier→grants) and the
  `auth/authorize.go:101-114` `mcp:*` tier-default fallback. `mcp:*` becomes an
  explicit role grant like `interact`/`admin` already are (`4/9:192`) — so the
  seeded default roles MUST cover the agent's full toolset (no fallback to catch a
  gap; a missing grant = the tool is denied, loud).
- **REPLACE** `auth/policy.go` `AuthorizeStructural` folder-prefix containment
  with the `acl` **scope glob** already evaluated in `authorize.go`: the seeded
  `folder:<path>` rows are scoped to the subtree, so containment becomes DATA (a
  scope glob), not a code prefix-walk. `ownsFolder` twins (`routd/server.go:337`,
  `runed/server.go:73`) fold into the same scope match.
- **KEEP** as a resource limit (not authz): `CheckSpawnAllowed`/`MaxChildren`
  (`auth/identity.go:54`) — decoupled from tier, a plain per-path cap.

## The tier-removal blast beyond authz (biggest risk — audit first)

`tierOf` (`container/runner.go:70`, a second depth→tier copy) drives THREE
spawn-time decisions that are NOT acl authz and would silently break or open up:

- **Egress (security-sensitive).** `tierOf ≤ 1` appends `*` to the crackbox
  allowlist — unconstrained network for operator bots (`runner.go:184`). Decoupled
  wrong, either everyone gets open egress (leak) or operator bots lose network.
  → move to an explicit grant (`egress:*` / reuse `network_rules`), seeded on the
  owner role, never derived from depth.
- **Web vhost mounts.** `tierOf ≤ 2` gets RO whole-`/pub` + a writable
  `public_html`/`private_html`; tier 3+ gets none (`runner.go:614,622,810`,
  spec `5/V`). → a `web:publish` grant, seeded on owner/member roles.
- **`share_mount`** is ALREADY grant-driven (`runner.go:553`) — the template for
  the two above.

None of these can be deleted with `Resolve`/`DeriveRules` until their tier gate is
replaced by a grant, or a decoupled path silently loses egress/web. This is the
first audit of phase 2, ahead of touching the evaluator.

## Rename (group / folder → path)

Concept-only where wire/DB identity is at stake; full where the compiler guards.

- **Internal Go (compiler-caught, safe):** `core.Group`→`core.Path`, `Folder`
  fields→`Path`, `groupfolder`→`pathloc` (or keep pkg, rename symbols),
  `GroupsDir`→`PathsDir`. ~308+240 files, mechanical.
- **MCP tool names (wire → migration broadcast):** `register_group`→
  `register_path`, `delegate_group`→`delegate_path`, `escalate_group`,
  `set_group_open`, `observe_group`, `list_groups`, arg `groupFolder`→`path`.
- **Env (container, per-turn ephemeral):** `ARIZUKO_GROUP_FOLDER`/`_NAME`/
  `_PARENT`→`ARIZUKO_PATH_*`; update the in-container self-skills that read them.
- **DB (persistence identity):** `groups` table + `folder`/`group_folder`
  columns + FKs. **DECISION (see Open-Q 5):** full rename via migration + view
  alias, OR keep table/column names and rename concept-only in code+docs. Default
  recommendation: **concept-only for DB** (tables stay `groups`/`folder`), full
  rename everywhere else — the honest-minimal move; a physical DB rename buys
  nothing an alias doesn't.

## Phasing (ship without a big-bang outage)

1. **Spec** — this doc; reconcile `4/9` (grant-option + delegation, tier removed),
   `3/5` (tier×action defaults — retire), `4/19` (`DeriveRules` — retire), `5/A`
   (coordinate=path, capability=role), `GRANTS.md`, CLAUDE.md/ARCHITECTURE/
   SECURITY/ROUTING tenancy language.
2. **Grants code** — in order: (a) audit + move the non-authz tier readers (egress,
   web-vhost) to explicit grants (§blast); (b) make the agent a first-class `acl`
   principal — seed `folder:<path>` rows at path-create, spawn resolves grants from
   `acl` not `DeriveRules`; (c) `grant_option` column + `Delegate` subset-check +
   default-role seeding; (d) delete the coupling trio. Tests: deep path + broad
   role; delegation chain narrows; re-grant without option blocked; solo create =
   working agent, zero config; egress/web survive tier removal.
3. **Rename** — mechanical group/folder→path (Go → MCP → env → docs); DB
   concept-only.
4. **Docs + web + migration** — repo UPPERCASE + `template/web/pub` grants/scopes/
   env/mcp pages; migration bump + broadcast (tool-name change); CHANGELOG.

Each phase ships green + verified before the next.

## Phase-2 cutover — strangler-fig order (turn-key)

Not a big-bang. Add the new path ALONGSIDE the live one, prove equivalence, then
remove the old. Every step compiles + full-suite green.

**Entanglement found (2026-07-30):** `Authorize` already expands the agent's
`folder:<path>` principal and reads its `acl` rows (`auth/authorize.go`
`expandPrincipals` + `ACLRowsFor`). So seeding `folder:<path>` `acl` rows is NOT
inert — it changes live authz. Therefore seeding and the Authorize-source flip
must land in ONE step, gated behind equivalence tests, not two.

Order:

1. **DONE (2a/2b):** `grant_option` column + `auth.Delegate` + operator-as-root.
2. **Seed + flip, together, behind tests.** A `SeedFolderGrants(folder)` writes the
   current `deriveFolderGrants(folder)` bundle as `folder:<path>` `acl` rows
   (grant_option on own subtree) at path-create/spawn; SAME step makes
   `turnAuthorize` prefer those rows. Test: for a matrix of (folder, tool), the
   new acl-sourced decision == the old `DeriveRules` decision, EXACTLY. Only merge
   when the equivalence test is green — it is the safety net.
3. **Wire `Delegate` at spawn + `add_acl`.** A parent seeds a child ⊆ its own
   grant-optioned rows; non-operator `add_acl` calls `Delegate` first. (Now safe:
   step 2 gave principals real grant-optioned rows to delegate from.)
4. **Egress + web off `tierOf`.** Seed `egress`/`web:publish` grants on the owner
   bundle; `runner.go:184/614/622/810` read the grant, not `tierOf`. Test: a
   former-tier-1 bot still gets `*` egress via its grant; a former-tier-3 gets
   none.
5. **Delete the trio.** Remove `Resolve`/`DeriveRules`/`AuthorizeStructural` +
   `ARIZUKO_TIER` + the `tierOf` copy. All callers now read `acl`. Full suite +
   e2e green.

The equivalence test in step 2 is what lets this be incremental instead of a
leap: the old and new authz answers must match before the old is removed.

## Safety audit — NO-GO as first scoped (2026-07-30)

An adversarial pre-flight audit ruled the cutover **not safe to execute** as first
drafted and corrected the plan. Must-fix-first, in order (only then does step 2 ship):

1. **F1 — DONE (commit 98b81c5e).** `role:operator` had no base row in a fresh
   `routd.db` and `grant_option=0` on migrate-split → the delegation root was empty.
   Migration `0022` seeds it WITH GRANT OPTION, ordering-safe vs `migrate_split`.
2. **The equivalence baseline is INADEQUATE.** `grants/equivalence_baseline_test.go`
   calls `DeriveRules`+`CheckAction` directly, bypassing `Authorize`/
   `AuthorizeStructural`. It pins 7 tools with a nil `RouteSource`. Before step 2 it
   MUST also cover: `AuthorizeStructural`'s ~20 gated tools at multiple containment
   depths; a populated `RouteSource` (`platformRules`); a per-folder acl-overlay row
   on tier defaults; the `mcp:*` fallback guard (`auth/authorize.go:101-114`). And it
   only pins the OLD side — it must become an old-vs-new DIFFERENTIAL once seeding
   lands (and survive `DeriveRules`' deletion).
3. **`deriveFolderGrants` (`routd/mcp.go:531`) is a MISSED reader — rewrite it in the
   SAME commit as the Authorize flip.** It feeds both the socket rules (`toolGrant`)
   AND runed's `in.Grants` (which after step 4 also drives egress/web), and reads acl
   via `ListACL("folder:"+folder)` — **exact-principal match only, no `Ancestors`
   role-expansion, no wildcard rows, no deny-wins lattice** — materially weaker than
   `AuthorizeWith`. Left as-is, a role-INHERITED grant passes the live gate but FAILS
   `deriveFolderGrants`' exact-match rules → the tool is silently unreachable
   (`toolGrant`, `agent_gate.go:61`, requires BOTH). Collapse `dashd/tools_admin.go`'s
   duplicate `DeriveRules` call into the same renderer.
4. **Root-is-a-tier-scalar (~10 sites) needs an `is_root`/role predicate**, not
   deletion: `ipc/inspect.go:13,27,62`, `ipc/ipc.go:1998/2065/2118/2157/2245/2202`,
   `agent_gate.go:50`, `acl_resource.go:203`, `route_tokens_resource.go:199,202`.
   Drop them blindly and the operator bypass either vanishes (containment hits root)
   or over-applies (deep folder treated as root).
5. **Non-authz tier readers need explicit replacements, not deletion**: the ~8 resource
   files' inline `auth.Resolve`+`AuthorizeStructural` (each a distinct scope-glob
   rewrite; confirm `ipc/ipc.go`'s are still live vs superseded by resreg);
   `prompt.go:154` (tier rendered into the agent prompt); `dashd/tools_admin.go:40`
   (display); `runner.go:184/614/622/810` (egress + web-vhost → grants); and
   `ARIZUKO_TIER` which `ant/skills/self/identity.md` + `ant/CLAUDE.md` read → needs a
   migration-broadcast, not a silent drop. Decide (don't skip) whether `ownsFolder`
   folds into acl scope-glob as the spec claims.

Verdict: incremental via strangler-fig IS the right shape, but steps 2–5 are each
larger than first written. Execute 1 (done), then 2+3 together behind the expanded
differential, then 4, then 5 — never the flip before the differential is green.

## Progress (2026-07-30, evening)

- **Item 1 — DONE** (`98b81c5e`): operator delegation-root seeded WITH GRANT OPTION.
- **Item 2 — DONE**: equivalence oracles green — tier→tool (`grants/equivalence_baseline_test.go`),
  `AuthorizeStructural` containment (`auth/structural_baseline_test.go`), platform
  verbs. The old-side surface is fully pinned.
- **Item 3 differential — GREEN** (`8d1c8914`): `SeedFolderGrants` translates the
  tier bundle into acl rows; `folderGrantsFromACLOnly` renders grants from acl alone;
  the differential proves acl-sourced == `DeriveRules` decisions EXACTLY across 4
  tiers × 10 tools incl platform verbs. **The grant-surface flip is now provably
  safe.** Also fixed the dual-path read (`674db521`): `deriveFolderGrants` now expands
  role membership, matching the live gate.

**Flip attempted (2251c4c5) — REVERTED, finding captured.** Wiring `deriveFolderGrants`
to seed the tier bundle per-folder then read acl-only is decision-correct (differential
green) but **the persistence model is wrong**: it dumps ~26 tier-default rows onto every
`folder:<path>` principal, which `list_acl` surfaces (a folder's grant list balloons from
its overrides to the full default set) and it writes on every turn. `deriveFolderGrants`
is back to `DeriveRules`-base + the audit-#4 role-expanded overlay (the two gates agree);
`SeedFolderGrants` + the differential stay as proven building blocks.

**The correct grant-surface flip = ROLE-based seeding.** Seed the tier bundles ONCE onto a
few `role:<tier>` (or `owner`/`member`/`reader`) principals; bind each `folder:<path>` to
its role via an `acl_membership` edge; `deriveFolderGrants` reads `folder:<path>` +
`Ancestors` (already does — audit #4) and gets the bundle via role expansion, with NO
per-folder grant rows. Then `list_acl(folder)` shows only folder-own overrides (role rows
live on the role principal), and there's no per-turn write. The binding-by-tier is the last
tier-read on this surface; removing it (role assigned at invite/create, not derived) is the
final step. THEN drop the `DeriveRules` base.

### Grant-surface flip — ATTEMPTED (`600fc408`) then REVERTED (`0f8d956f`)

The role-based flip shipped, then an adversarial review found it broken (three confirmed
bugs) and it was reverted. What the flip got wrong — the requirements the CORRECT flip must
meet:

1. **Test the REAL path.** The differential drove `SeedFolderGrants`, which the shipped
   `deriveFolderGrants` never calls (it used `SeedTierRoles`+`PutMembership`+
   `PlatformRulesForFolder`+`folderGrantsFromACLOnly`). "Proven safe" validated dead code.
   The correct flip's equivalence test must diff the ACTUAL `deriveFolderGrants` old-vs-new.
2. **Deny precedence.** `grants.CheckAction` is last-match-wins and `store.ACLRowsFor` has NO
   `ORDER BY`; the index returns `folder:` before `role:`, so an operator deny sorted BEFORE
   a role allow and was masked (confirmed repro; live consumer: the `share_mount` RO gate,
   no `db.Authorize` pairing). The correct flip must render denies LAST (or add ORDER BY /
   deny-wins in the render).
3. **No blind rebind.** `deriveFolderGrants` re-bound the folder to its DEPTH role every
   call, so a folder could only be widened, never restricted below depth — "decoupled from
   location" was false. The role must be assigned once (create/invite), not re-derived per
   read.
4. `SeedTierRoles` is INSERT-OR-IGNORE-only → a tightened bundle never revokes (stale role
   rows survive). 5. `dashd/tools_admin.go` renders from `DeriveRules` directly — a second
   sink that drifts from the socket (pre-existing; the flip widened the gap).

`SeedTierRoles`/`SeedFolderGrants`/`PlatformRulesForFolder`/`folderGrantsFromACLOnly` remain
as UNWIRED building blocks. `deriveFolderGrants` is back to `DeriveRules`-base + expanded
overlay (deny appended last = correct). Full routd suite green. The equivalence oracles
(`TestIntegration_*`) still stand. Recorded in `BUGS.md`.

## Open questions

> **Most of these are now closed by §"Resolved model" (2026-07-30).** Root is a
> grant not a location (Q on root-bypass); tier fully dissolved (Q1); grants by
> lineage-delegation with a single unprivileged default (Q on default roles — the
> `role:owner/member/reader` framing below is SUPERSEDED by one `role:unpriv` +
> delegation); mounts + skill-mod are grants with global-skills operator-only.
> Genuinely still open: the exact `AuthorizeStructural`→acl-scope-glob per-tool
> mapping; the rename depth (Q5); `ARIZUKO_TIER`/prompt/dashd display replacements
>
> - skill migration; and the staged rollout sequence.

1. **Non-authz tier uses — RESOLVED (see §blast + §Resolved model).** `tierOf`
   drives egress (→ a `mount`/egress grant) and web-vhost mounts (→ a web grant);
   `MaxChildren` stays a plain cap; root-by-location → the root grant. Remaining:
   grep for any OTHER `ARIZUKO_TIER` / `tierOf` / `Resolve` reader the map missed.
2. **Grant-option vs action lattice.** Column (chosen) vs a lattice rung
   (`admin`⇒may-grant)? Column keeps delegability orthogonal to coverage; a rung
   conflates "can do admin" with "can hand admin out." Column wins.
3. **Self-escalation with no ceiling.** With tiers gone, a bug that seeds a child
   `(*, **, grant_option=1)` self-escalates. Backstop: the subset-check makes a
   child unable to grant _more_ than the granter holds — so escalation requires
   the granter to already hold `*` WITH option, i.e. be operator-equivalent. Is
   subset-⊆-held enough, or do we want a per-principal **non-delegatable ceiling**
   grant (which must NOT reintroduce depth)?
4. **Default-role re-coupling.** Confirmed line: default role is a one-shot seed
   at path-create, never a live parent-walk (§Default roles). Verify no code path
   re-reads the parent's roles at authz time.
5. **DB rename depth.** Full physical `groups→paths` migration vs concept-only.
   Recommendation: concept-only for DB. Confirm no external tool queries the
   `groups` table by name.
