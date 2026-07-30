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

## Open questions

1. **Non-authz tier uses — RESOLVED (see §blast).** `tierOf` drives egress
   (tier≤1→`*`) and web-vhost mounts (tier≤2), plus `MaxChildren`/`WorldOf`. All
   re-expressed as explicit grants / plain caps before `Resolve` dies. Remaining:
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
