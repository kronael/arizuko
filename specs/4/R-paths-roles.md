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

8. **Containment = grant SCOPE, not `AuthorizeStructural`.** The ~20 tools the
   structural cascade gated (`register_group`, `routes`, `network_*`, `schedule_*`,
   `add_acl`, outbound send, …) become plain `mcp:<tool>` grants whose **scope glob
   IS the containment**: holding `mcp:register_group` scoped `acme/**` lets you
   register under `acme`, nothing else. `AuthorizeStructural` (and the depth caps
   inside it) is deleted; the acl scope-match already in `AuthorizeWith` does the
   whole job. A grant delegated for a subtree carries that subtree as its scope, so
   the delegation chain sets containment by data.

9. **The group→path RENAME is a SEPARATE later pass — not part of this cutover.**
   4/R lands the authz model (root-grant, tier-dissolve, lineage delegation) FIRST;
   the mechanical `group`/`folder`→`path` rename (300+ files, DB, MCP tool names,
   env, in-container skills) is its own spec + pass AFTER the authz change is live
   and stable. Coupling a huge rename to a risky auth cutover is how both break.
   `4/R` may say "path" conceptually; the code keeps `folder`/`group` until the
   rename pass.

10. **`ARIZUKO_TIER` and every tier DISPLAY are dropped, not replaced with a
    number.** No tier scalar survives. The agent prompt drops the tier line (or
    shows a short grant summary); `dashd` shows the group's granted capabilities,
    not "tier N"; `ARIZUKO_TIER` env is removed and the 4 in-container skills that
    read it are updated via the migration-broadcast. Identity the agent sees =
    world + path + its grants, never a rank.

11. **Staged rollout (each behind the equivalence tests, never big-bang):**
    (a) root-as-grant + an `is_root` predicate replacing the `id.Tier==0` checks;
    (b) `AuthorizeStructural`→acl scope-glob (decision 8); (c) the corrected
    grant-surface flip (role-sourced grants, denies-last, assign-once, test the REAL
    `deriveFolderGrants` — per `BUGS.md`); (d) egress/web off `tierOf` → grants;
    (e) delete `Resolve`/`DeriveRules`/tier + the `ARIZUKO_TIER` skill migration.
    Only after (a)-(e) are green does the tier axis actually leave the tree.

12. **Inspect is the READ face of the configurable grants.** Grants are already a
    configurable resreg resource (`acl` — add/remove/list, both MCP + REST). "Inspect
    grants" is just its **list face scoped to the caller's own effective grants** —
    not a bespoke tool, an extension of grants-being-configurable. So an agent that
    can configure grants (delegate down its subtree) uses the SAME resource to read
    them; a group with only the read grant sees its own. Passively too: the
    advertised tools in `tools/list` reflect the grants (a tool without its grant
    isn't offered) and the prompt summarizes them — all three read one `acl`, so
    they agree by construction.

13. **Egress: sensible errors + advertised proxy.** A blocked (non-allowlisted)
    egress attempt MUST return a clear error naming the **egress proxy** as the
    blocker — not a bare `403` on every path that reads like the TARGET's own auth
    gate (the exact misdiagnosis `ant/CLAUDE.md` §"Network egress" warns about). And
    the environment **advertises that the agent is behind an egress proxy** (in the
    prompt / a known env marker) so a denial is understood as the allowlist, not the
    site. Egress reach itself is a grant (decision 6/11d): a host on the group's
    egress grant passes; anything else is refused with the clear proxy error.

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

## Default roles (per decision 7 — one unprivileged default)

Only TWO seeded roles exist; there are no per-tier bundles:

- `role:operator` — `(*, **, allow, grant_option=1)`, the ROOT grant (decision 1).
  Held by the operator, invoked via `/root`; it is the source of every delegation
  chain and the only thing that can seed a world's first real grants. NOT a folder
  position — no root group.
- `role:unpriv` — the floor (reply/send in own thread; essentially no management
  verbs), `grant_option=0`. **Every group is born a member of this**, regardless of
  where it sits. This is the whole zero-config default.

Everything above the floor is EXPLICIT delegation from the creating group or a
lineage ancestor (decisions 3 + §Delegation), bounded by `grant_option` subset — a
group has exactly the grants its lineage handed it. Solo stays zero-config because
the operator's `create` (root grant) seeds the world's grants once; nothing is
implied by depth.

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

### Grant-surface flip — SHIPPED, corrected (`be724eed`)

Attempted (`600fc408`), reverted after adversarial review found 3 bugs (`0f8d956f`), then
re-shipped correctly with ALL FIVE addressed. `deriveFolderGrants` now sources grants from
the acl/role graph — `role:tier<N>` bundles seeded at DB open (`SeedTierRoles`), the folder
assigned its role ONCE, grants rendered from `folderGrantsFromACLOnly` (denies last) +
`PlatformRulesForFolder`. No `DeriveRules` base on this path.

The 5 bugs and their fixes:

1. **Real-path test** — the differential (`TestIntegration_GrantSourceDifferential`) drives
   the ACTUAL `deriveFolderGrants` and proves it equals old `DeriveRules` across tiers×tools.
2. **Deny precedence** — `folderGrantsFromACLOnly` partitions denies LAST, so an operator
   deny beats a role allow regardless of `ACLRowsFor` order (`share_mount` RO gate safe).
3. **Assign-once** — `hasTierRole` guards the bind, so an operator rebind sticks and a folder
   can be restricted below depth (`TestIntegration_RoleDecouplesDepth`).
4. **Prune** — `SeedTierRoles` deletes then reseeds each role, so a tightened bundle revokes.
5. **dashd parity** — `dashd/tools_admin.go` still renders from `DeriveRules` (BUG5, pending —
   a separate cross-daemon commit; today identical since roles == the tier bundles).

`list_acl(folder)` stays clean (role rows on the role principal). Full routd + auth + store

- grants + lint green.

**Implementation status of the staged rollout (§Resolved model 11):**
(a) IsRoot decoupling — DONE (`14eb9504`). (c) grant-surface flip — DONE (`be724eed`).
**Delegation mechanism — DONE** (`b7ec8232`+`14cd52a5`): role bundle allow-grants carry
`grant_option`; `auth.Delegate` wired into `add_acl` so a non-root writer may only grant a
row it holds WITH the option (root bypasses via `IsRoot`), and the scope-check bounds
delegation to the granter's scope. A group can't hand out authority it wasn't delegated.

(d) egress + web-mount off `tierOf` — **REVERTED** (`edd42181` → `3a8b8291`, CTO audit
2026-07-31). The flip was NOT equivalence-preserving: `container.tierOf` (`count("/")+1`)
and `auth.Resolve().Tier` (`min(count,3)`, floored to 1) diverge below the top level, so
folding the gate into the tier-1 grant bundle gave every 2-segment sub-group unconstrained
egress and broke `/root` web mounts (`CheckAction(["*"], "web:publish")` is false —
`matchGlob` stops `*` at `:`). The tier scalar CANNOT express the old `tierOf ≤ 1` egress
predicate. Egress/web are back on `tierOf` (the accurate, equivalence-preserving state).
The real (d) rides the **model-shift + backfill** below: write explicit per-folder
`egress`/`web:publish` grants that reproduce the `tierOf` predicate EXACTLY, with an
equivalence test enumerating real folder paths (not the tier scalar), THEN delete `tierOf`.

Remaining (audited 2026-07-31, exact live non-test call sites):

- **(b) `AuthorizeStructural` → grant scope** — ~9 call sites + ~15 raw `id.Tier`
  containment comparisons inside `auth/policy.go`. The tool-gating half already moved to
  grants; the subtree-containment half becomes `ownsFolder(callerFolder, target)` (depth-
  free) with the per-tool nuances (register=direct-child, delegate=strict-descendant,
  tasks=own-world) encoded as scope rules. Root bypasses via `IsRoot`.
- **(e) delete the tier scalar** — `auth.Resolve` (10 sites), `DeriveRules` (3: the bundle
  source survives, the `auth/authorize.go:113` mcp:\* fallback + `dashd/tools_admin.go`
  display go), `ARIZUKO_TIER` (emit at `runner.go:828` + 2 skills read it → migration-
  broadcast). Blocked on (b).
- **model-shift** — create-time delegation (operator/parent delegates SUBTREE-SCOPED grants
  to a new group at create; absent) + a **backfill migration** (delegate tier-equivalent
  scoped grants to every existing folder; absent). Prerequisite for (b) to be a true
  containment-by-scope rather than an `ownsFolder` shim.
- BUG5 dashd parity; then the docs/examples/products rewrite (root UPPERCASE + web still
  describe tiers).

The model-shift + backfill change what every agent may do in every deployment (krons/
sloth/marinade) — they land as one REVIEWED migration with e2e, not a session tail.
`DeriveRules` stays until (b)/(e).

## Phase (b) cutover design — containment by scope (2026-07-31, awaiting sign-off)

The design risk — "can a scope glob express every containment shape `AuthorizeStructural`
encodes?" — is **retired**. `auth/acl.go matchPattern` splits on `/` and `**` matches
zero-or-more segments (`F/**` matches `F` itself), so each shape maps to a glob relative to
the caller's folder `F`:

| structural shape (today) | scope glob      | tools                                                                                                           |
| ------------------------ | --------------- | --------------------------------------------------------------------------------------------------------------- |
| self-or-descendant       | `F/**`          | reset*session, fork_topic, network*\*, routes, set_group_open, set_observe_window, inspect_tasks, outbound send |
| own-folder only          | `F`             | schedule_task (was tier-2 `TaskOwner==Folder`)                                                                  |
| direct-child only        | `F/*`           | register_group                                                                                                  |
| strict-descendant        | `F/*/**`        | delegate_group                                                                                                  |
| own-world                | `WorldOf(F)/**` | get/set_grants, list_acl, invite_create/revoke, add/remove_acl, schedule_task (tier-1)                          |

`AuthorizeStructural` does TWO orthogonal jobs; the cutover splits them:

1. **Magnitude gate** ("tier ≥ 2 cannot register_group") → **the tool grant is simply absent
   from that folder's bundle.** Already true: `register_group` lives in the tier-0/1 bundle,
   not tier-2/3. No new code — the grant surface already carries "may I use this tool at all".
2. **Containment gate** (the folder-prefix checks) → **the grant's scope glob**, matched by
   the `matchPattern(r.Scope, scope)` already in `AuthorizeWith` step 4. This is the part the
   backfill must seed.

**The backfill migration (routd `0023`, the reviewed fleet change).** For every existing row
in `groups`, compute `T = Resolve(folder).Tier` and, for each tool `T` holds, write a
`folder:<path> → mcp:<tool>` acl row scoped to that tool's containment glob (table above),
`grant_option=1` for allows. Also seeds the reverted step (d): `egress` scoped `F/**` for
`tierOf(F) ≤ 1`, `web:publish` scoped `F/**` for `tierOf(F) ≤ 2` — reproducing the `tierOf`
predicate EXACTLY (the C1 lesson: never route it through the tier scalar). Idempotent
(skip-if-present), and a `down` that deletes exactly the rows it wrote. After backfill every
folder's authority is data; `AuthorizeStructural` becomes dead and is deleted, its ~7 call
sites relying on `AuthorizeWith`'s scope-match.

**Create-time delegation.** New groups can't wait for a migration: at `register_group` /
`SetupGroup`, the creator delegates the child's grants via `auth.Delegate` (subset of held,
scoped to the child subtree) — the same rows the backfill writes for existing folders, but
minted live from the parent's held authority. This is the steady-state path; the backfill is
the one-time catch-up for pre-existing folders.

**The equivalence test (the safety net that was missing — CTO 2026-07-31).** Enumerate REAL
folder paths at each depth × every tool × representative targets (self, child, descendant,
sibling, parent, cross-world) and assert `AuthorizeWith(post-backfill scopes)` == the current
`AuthorizeStructural` decision for all. Keyed on folder PATHS, never the tier scalar (the hole
that let step (d) ship broken). Green here is the gate to deleting `AuthorizeStructural`.

**Rollout + revert valve.** (1) land `BackfillScopes` helper + equivalence oracle
(inert) — DONE (`6d562598`). (2) land `BackfillFolderGrants`, additive, wired at
`routd.Open` — DONE (`18ce8d93` + finding-fixes). (3) oracle green on REAL tier/depth
pairs — DONE. (4) flip call sites to scope-match — **DONE (`ea50df11`)**: all 7 sites
(5 resreg direct + 3 hand-authored ipc via the `StoreFns.Containment` seam) call
`auth.AuthorizeContainment`; egress-cap fork resolved to (B). `AuthorizeStructural` retained
as the revert valve (+ the oracle). (5) delete `DeriveRules`/`Resolve`/`ARIZUKO_TIER`
(phase e) — REMAINING. Deploy to krons only, watch, then fleet — NOT yet deployed.

**Flip blockers — step (4) is NOT "just delete `AuthorizeStructural`" (adversary review
2026-07-31).** Two findings reshape it into the full grant-model migration; do NOT flip
until both are designed and tested:

1. **`role:tier<N>` rows are scope `**`(M6) → they DEFEAT the backfilled containment.**
Once a call site does`db.Authorize(scope=TARGET)`, the folder's `role:tier<N>`membership contributes`{mcp:<tool>, scope:"**"}`rows that`matchPattern("**", any)`allows unconditionally — so the narrower`folder:<path>`scoped row is irrelevant. Worst
case: a tier-3 agent`reply`-ing into an unrelated tenant's chat is ALLOWED. The flip
therefore MUST replace `role:tier<N>`membership with per-folder scoped grants and remove
(or radically narrow) the`\*\*`role rows — this is decision 7's`role:unpriv`-plus-explicit
   -delegation end state, not a tweak. The role bundles were always transitional scaffold.

2. **The backfill covers only folder-scoped containment, not the platform verbs.** `post`,
   `like`, `dislike`, `delete`, `edit`, `forward`, `quote`, `repost` are `(jid=<platform>:*)`
   param-scoped in the bundle, so the intersection gate `CheckAction(bundle, tool, nil)`
   skips them (nil params never match a jid rule). Their folder-containment (authorizeOutbound
   self-or-descendant) is currently held ONLY by `AuthorizeStructural`. When the `**` role
   rows go (blocker 1), the full backfill must express BOTH axes for these verbs: the jid
   param (which platform) AND the folder scope (which subtree) — else they either escape
   containment or lose availability cross-folder.

Consequence: step (4) is the coherent "role bundles → per-folder fully-scoped grants" cutover
(all tools, both axes), landed as one reviewed migration with the equivalence oracle extended
to cover `db.Authorize`-post-flip (not just `BackfillScopes`). Steps (1)-(3) are the safe,
additive, shipped foundation; (4)-(5) await this design + sign-off.

**Verified flip approach (avoids the blockers — 2026-07-31).** Blocker 1 only bites if
containment routes through `db.Authorize(scope=target)`, which consults the `**` role rows.
The fix is a DEDICATED containment check that reads `folder:<path>` scoped rows ONLY — never
role rows — so the `**` bundle scope is irrelevant to containment. Phase (b) then stays a
pure containment move; the role-bundle teardown is deferred to phase (e), matching the spec's
own b-then-e staging (do NOT conflate them).

- **Magnitude is already enforced separately** — confirmed at `routd/groups_resource.go:166`
  (`toolGrant(rules, authorize, …)` runs BEFORE `AuthorizeStructural`), and the resreg Gate
  runs `db.Authorize(mcp:<tool>)` at every resource site. So `AuthorizeStructural`'s magnitude
  `if id.Tier >= N` arms are REDUNDANT with the bundle gate — a containment-only replacement
  loses nothing. (The `ipc/ipc.go`+`ipc/inspect.go` sites must be re-verified the same way
  before flipping — check a bundle/`CheckAction` gate precedes each `authzStructural`.)
- **New check:** `AuthorizeContainment(st, callerFolder, tool, target, isRoot) error` — root →
  nil; else allow iff a `folder:<callerFolder>` acl row `mcp:<tool>` has a scope covering
  `target` (`ScopesMatch`). Reads folder rows via a principal-scoped query; role rows excluded.
- **Backfill extension:** outbound verbs (`post`/`like`/`forward`/… `send`/`reply`) have
  containment self-or-descendant (`F/**`) INDEPENDENT of magnitude, but the bundle-intersection
  gate skips them (jid-param rules don't match `CheckAction(…, nil)`). The backfill must write
  their `F/**` containment scope UNCONDITIONALLY (the jid/magnitude gate stays orthogonal at
  runtime) — else the flip denies legitimate cross-subtree sends.
- **Then:** flip the 7 call sites `AuthorizeStructural(id,tool,tgt)` → `AuthorizeContainment(st,
id.Folder,tool,tgt,id.IsRoot)`, extend the oracle to assert the wired path == the old decision,
  keep `AuthorizeStructural` one release as the revert valve, delete it after krons soak.

This keeps phase (b) reversible and role-bundle-agnostic; phase (e) (delete role bundles +
`DeriveRules`/`Resolve`/tier) is the separate follow-on. **Phase (b) wired `ea50df11`; phase
(e) — the tier-scalar teardown — remains.**

**Mechanism BUILT + PROVEN (inert), 2026-07-31.** `auth.AuthorizeContainment` shipped
(`fd68ac8a`) reading `folder:` rows only; `TestAuthorizeContainment_MatchesStructural` seeds
via the REAL `BackfillFolderGrants` and proves it equals `AuthorizeStructural` for every
magnitude-granted tool × folder(depth 1-4) × target. The backfill writes `**` no-containment
rows + unconditional outbound `F/**`, and `folderGrantsFromACLOnly` excludes backfill-marked
rows so the magnitude firewall is unpolluted (`410e8dc9`). What's proven is the READ path; the
inert mechanism is turnkey.

**Wiring prerequisites (before flipping the 7 call sites — each a real dependency, not
polish):**

1. **Create-time delegation** — `AuthorizeContainment` needs the caller folder's scoped rows
   to exist. The backfill covers PRE-EXISTING folders at `Open`; a NEWLY registered folder has
   none until the next `Open`, so `register_group`/`SetupGroup` MUST delegate the child's
   scoped grants at create (via `auth.Delegate`, subset-of-held). Without it, wiring denies all
   sub-operations of any folder created after boot. This is the steady-state half of the model
   the backfill only catches up.
2. **ipc call-site magnitude verification** — the resreg sites (groups/routes/network/acl/
   tasks) run `toolGrant`/`db.Authorize` magnitude BEFORE `AuthorizeStructural` (verified), so a
   containment-only swap is safe. The `ipc/ipc.go`+`ipc/inspect.go` sites must be confirmed the
   same before their swap — else the swap drops magnitude.
3. **Residues** — `register_group`'s "worlds are CLI-only" (root, single-segment target) is a
   non-containment rule `AuthorizeContainment` (root-bypass) doesn't express; preserve it inline
   at the register site.
4. **Test seeding** — resreg containment tests must run `BackfillFolderGrants` (or delegate) for
   the folder before asserting, since the gate now reads data.

Then: swap the 7 sites, extend each resource's containment test through the wired path, keep
`AuthorizeStructural` one release as revert valve, krons-soak, delete. Phase (e) follows.

**HARD BLOCKER found by attempting the wiring (2026-07-31) — a POLICY decision, not a bug.**
Wiring `groups`/`routes`/`tasks` flipped clean (magnitude is redundantly gated by
`toolGrant`/`db.Authorize` there), but `network_*` FAILED `TestNetworkRulesMCP_TierGateDeniesTier2`
and the swap was reverted. The test grants `network_allow` via an operator ACL row yet expects
DENIAL: egress management carries a **hard tier cap** (`tier ≥ 2` can NEVER manage egress) that
`AuthorizeStructural` enforces ALONE and that **operator grants cannot widen** — documented in
`network_rules_resource.go`'s own header. This breaks the flip's core premise ("magnitude is
separately gated, so a containment-only replacement loses nothing"): for egress (and likely
`add_acl`/`invite_*`/`get_grants` — every "tier ≥ 2 can't manage X" arm), the magnitude cap
lives ONLY in `AuthorizeStructural`, is NOT redundant with `db.Authorize`, and is a HARD ceiling.
A containment-only `AuthorizeContainment` LOSES it → an operator-granted deep folder gains egress
management the structural cap forbids. That is a real security loosening.

**RESOLVED: the fork is decided by decision 8 — it's (B).** Decision 8 already says
"`AuthorizeStructural` (and the depth caps inside it) is deleted; containment = grant
scope." The hard tier caps ARE the "depth caps inside it" the spec deletes. So egress/acl/
invite become plain scoped grants: default-deny (not in the unprivileged bundle; only the
operator holds `*` and can delegate them), and an operator's explicit scoped grant to a
folder DOES take effect (the folder manages egress within that scope). This is not a scary
loosening — non-operators still cannot self-grant (delegation requires holding the grant WITH
option, which they don't), so egress stays operator-gated; the change is only that an operator's
EXPLICIT delegation now works, which is the whole point of the grant model.
`TestNetworkRulesMCP_TierGateDeniesTier2` encodes the OLD tier behavior and must be updated
(operator-granted folder → allowed IN SCOPE, denied out of scope).

BUT the wiring still lands as a REVIEWED migration with e2e (this §, line ~488) — NOT a
session tail — because it changes fleet auth. The mechanism is built + proven; wiring (B) +
the test updates + phase (e) are the reviewed-migration payload. Fork options kept for the
record:

- **(A) Keep the caps** — reframe each hard cap as a grant only `role:operator`/root holds (e.g.
  `mcp:network_allow` is simply never in any non-operator bundle and never delegable), so the
  cap becomes data (absence of the grant) rather than a tier arm. Equivalence-preserving.
- **(B) Drop the caps** — 4/R decision 8 taken literally: if the operator grants a folder
  `network_allow` scoped to its subtree, it MAY manage egress there. Simpler, truer to "grants
  not tiers", but a deliberate loosening of egress/acl/invite control that must be signed off.

Until (A)/(B) is chosen, `AuthorizeStructural` stays wired for the hard-cap tools. The clean-flip
tools (`groups`/`routes`/`tasks`/outbound) could flip independently, but a split leaves two
containment mechanisms live — better to flip all 7 together once the policy is set. The
mechanism, backfill, create-time delegation, and fallback are all shipped + proven and wait on
this one decision.

## Open questions

> **DESIGN CLOSED — all resolved in §"Resolved model" (2026-07-30, decisions 1-13).**
> Root is a grant not a location; tier fully dissolved; grants by lineage-delegation
> with ONE unprivileged default (the `role:owner/member/reader` framing below is
> SUPERSEDED); mounts + skill-mod are grants, global skills operator-only;
> containment = grant scope (no `AuthorizeStructural`); the group→path rename is a
> SEPARATE later pass; `ARIZUKO_TIER`/tier displays dropped, not renumbered; grants
> inspected via the acl resource's read face (+ advertised tools + prompt); egress
> errors name the proxy + the proxy is advertised. The numbered questions below are
> kept for provenance; the resolved model overrides them. **What remains before
> `shipped` is IMPLEMENTATION (the staged rollout a-e), not design.**

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
