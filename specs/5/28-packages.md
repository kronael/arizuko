---
status: draft
---

# specs/5/28 — arizuko packages

Source-first package model: GitHub repos seeded into agent containers
at spawn. Packages ship skills (auto-injected into every group), agent
group seeds, and sidecar MCP servers. No registry, no OCI images — just
`go install`-style GitHub URLs with a declarative `package.yaml`.

## Today's state

Skills live in `ant/skills/` and are copied into every container at spawn
via `container/runner.go:seedSkills()`. External MCP servers are registered
per-folder in `MCP.json` (spec `5/13`). There is no standard way to package
and distribute skills, group templates, or integrated sidecars as a unit.

## The packaging cluster — who owns what

Packaging was split across five specs saying overlapping things. Canonical
division (this reconciles them):

| Concern                                                                                               | Spec                     |
| ----------------------------------------------------------------------------------------------------- | ------------------------ |
| **Package manager** — install / update / remove of a software unit; the distributor-managed lifecycle | **this spec (5/28)**     |
| Producer side — what a product/package _contains_ (`PRODUCT.md`, persona, skills)                     | `5/21`                   |
| Composition — a group blends an ordered LIST of products (mixin precedence)                           | `5/20`                   |
| State transport — moving a live agent's rows/data (`export`/`apply`, `pg_dump`-style)                 | `5/20` (mech. 1) + `5/8` |
| Compose fragment — one asset _kind_ a package ships                                                   | `5/27`                   |
| Prototype — a product instantiated at spawn (`5/29` Tier-3)                                           | `5/29`                   |
| OCI distribution envelope — rejected alternative                                                      | `5/30` (superseded)      |

The one-line rule: **5/28 is the lifecycle mechanism; 5/21 is the payload;
5/20 is state + how many products compose; 5/27/5/29 are asset kinds.** State
transport (5/20 mech. 1) and package distribution (here) stay separate concerns
— they meet only when a backup carries the manifest so a restore reinstalls the
same packages.

## Target shape

### Package manifest (package.yaml at repo root)

```yaml
name: sloth
version: 1.0.0
type: skill # skill | agent | sidecar
requires:
  - BINANCE_API_KEY # optional: warn if unset
  - BINANCE_SECRET
skills: skills/ # dir to copy to ant/skills/<name>/
group: sloth # for type: agent — folder name to seed
compose: compose.yml # for type: sidecar — compose fragment path
mcp: MCP.json # for type: sidecar — MCP registration snippet
apply: arizuko.yaml # optional: apply manifest
```

### Three package types

**1. Skill** — SKILL.md files only. No daemon. Install copies `skills/`
→ `ant/skills/<name>/`; rebuilt into the next agent spawn.

**2. Agent** — Full group folder seed. Contains `SOUL.md`, `skills/`,
optional `arizuko.yaml` apply manifest. Install seeds `groups/<folder>/`
(skip if exists), registers routes via apply manifest.

**3. Sidecar** — MCP server as Docker sidecar. Contains `compose.yml`
(the container), `MCP.json` snippet (registration), `skills/` (agent-side
SKILL.md). Install deploys compose fragment, registers MCP endpoint in
target group folders.

### Install flow

```
arizuko packages install github.com/kronael/sloth[@v1.0.0]
  1. clone/download → ~/.arizuko/packages/sloth/
  2. read package.yaml
  3. warn if requires: env vars missing
  4. copy skills/ → ant/skills/<name>/
  5. if type=agent: seed groups/<folder>/ (skip if exists)
  6. if apply: arizuko apply <instance> arizuko.yaml
  7. if type=sidecar: copy compose.yml → services/<name>.yml
                      register MCP.json with target group folders
  8. print: "needs restart if compose changed"
```

## Install lifecycle — distributor-managed

> **A declarative reconciler was drafted here and demolished** (codex, 2026-07-28;
> full critique `.ship/critique-cto-20260728.md`). It proposed one engine running
> `apply`/`export`/`remove` in three directions over resreg rows, with
> `provenance = ownership`. Nine fatal flaws — kept as the record of why this
> shape is wrong:
>
> - **Provenance was a missing schema.** A `proxyd_routes` row carries nothing
>   saying a package installed it; "the package's to delete" had no basis.
> - **Manifest-as-authoritative contradicts `5/8`** (SQLite is truth; YAML is
>   dump/restore). "delete extra" then either erases operator edits or can't.
> - **state→typed-verbs is not reversible** — a row can't reveal whether it came
>   from a verb, REST, a migration, or an agent; export can't reconstruct it.
> - **Export ≠ "reproduce a world"** — secrets, tokens, sessions, history are
>   excluded (`5/20`); generic registry export leaks bearer tokens (invites).
> - **The agent-session hatch annihilates determinism** — no plan, no
>   idempotence, no rollback; sandboxing stops host escape, not authorized
>   exfiltration or repeated side effects.
> - **"negated manifest" is gibberish** (inverse of `env.set`? of minted tokens?
>   of `seed_group` with accumulated history?).
> - Verb names didn't match resreg (`route`≠`proxyd_routes`, `grant`≠`acl`); the
>   shipped YAML engine does raw `DELETE+INSERT`, bypassing the canonical handler
>   / gate / audit.

The survivable model: install is a **deterministic phased plan through the owner
REST APIs**, with the package (the distributor) authoritative. No universal
reconciler, no per-row provenance schema — a package owns exactly the identities
its manifest declares.

### Phased install

Ordered phases, each idempotent, with health gates — not an unordered verb list:

1. **preflight** — resolve source to an immutable revision + content hash; check
   `requires:` env; detect name/route/port collisions; refuse if a dependency is
   absent.
2. **files** — skills, group seed, compose fragment, `.env` keys (atomic writes).
3. **restart/health** — bring up any new sidecar; wait healthy before routing to it.
4. **resources** — create routes/grants/etc. by calling the **owner REST API**
   (`POST /v1/routes`, `/v1/acl`, …) so each goes through its real handler, gate,
   validation, and audit (`5/17`). NOT raw DB writes.

Partial failure resumes from a durable operation journal (roll-forward), not a
fake global rollback — filesystem/compose/restart side effects can't be
transactionally undone (`5/8`).

### Distributor-managed — the package is authoritative

A package **declares its assets by identity**: skills, routes (by path), grants
(by key), a group folder, seeded files. Install and upgrade **write those,
overwriting whatever is there** — the distributor's version wins. No `owner`
column, no precedence rules, no three-way merge, no fork/detach verbs. The
manifest names what the package owns; there is nothing else to track.

- **A package owns exactly the identities its manifest declares.** Anything at an
  identity **no** package declares is operator-local and never touched — your own
  route at a different path is safe by construction.
- **remove** deletes the manifest's identities — `DELETE /v1/routes/<path>`, drop
  the skills/files — through the owner REST handler (`5/17`). Refuse if another
  package declares a dependency on this one.
- **upgrade** writes the new manifest over the old and deletes assets the old
  declared that the new drops. Overwrite, never merge.

### The upstream channel — the only "local" story

An agent or operator may change a package asset in place; it works until the next
upgrade **overwrites** it. Local edits are **provisional by design**. The durable
path is a clear channel telling the distributor which change to incorporate: the
agent submits the diff + rationale via the existing `issues` / `gh-issue` MCP,
and the distributor folds it into the next release — where it ships back as a
package asset.

```
local edit → next upgrade overwrites it → but the agent filed it upstream
     ↑                                                        ↓
     └──────  next release ships it as a package asset  ←─────┘
```

Local is the R&D edge; upstream is the durable form — `6/`'s agentic-
reimplementation loop. "Keep it local forever" = fork the package (run your own
distributor). Nothing an agent cares about is silently lost: an overwrite worth
keeping was already proposed upstream.

### Skills use the same model

Skills are distributor-managed too — a skill upgrade **overwrites** the local
copy, **retiring the `/migrate` 3-way merge** (`~/.claude/.merge-base/`
inline-merge). A local skill tweak worth keeping goes upstream as an issue, not
into an inline merge. Skills stop being a special case.

### Resolves `5/27` C2

`packages remove slakd` reads slakd's manifest, sees it declares `/slack/`, and
`DELETE /v1/routes` for that path through proxyd's REST handler — so the live
`proxyd_routes` table updates (not a regenerated JSON blob proxyd ignores). The
manifest names what to remove; no ownership guessing.

### Conditionals and logic

- **`requires:` / collision checks** are preflight predicates (typed, not a
  grammar). A route whose `requires` env is unset is simply not installed.
- **Real branching logic** is an explicit, **opt-in** agent setup action — NOT
  part of the declarative install, and carrying **no** idempotence/rollback/
  ownership guarantees. It runs a `5/29` setup prototype (sandboxed by crackbox,
  grant-gated, audited), invoked knowingly by the operator. Keeping it outside
  the install contract is the point.

### Now in scope (were "document in README")

Cross-package dependencies need real handling: versioned dependency declarations,
collision detection, reverse-dependency refusal on remove, and reference
semantics for a shared instance-global sidecar (one install, refcounted).

## Discovery and interconnection

**Discovery**: GitHub topic `arizuko-package`. No registry needed.

**Shared libraries**: existing `share/` mount (`/var/lib/share`
→ `groups/<world>/share/`) is the mechanism. Packages can seed files there.

**Interconnection**: via skills (agent invokes by SKILL.md name) and via
MCP (sidecar provides tool endpoints). No new protocol — MCP is the interface.

### Out of scope v1

Multi-env support (skill with `requires: {dev: [VAR], prod: [OTHER_VAR]}`).
Package registry or central catalog (GitHub topic search is discovery).
(Package dependencies moved IN scope — see "Install lifecycle" above; codex
2026-07-28 ruled README-only dependencies unserious.)

## What deletes

Nothing. This adds a distribution mechanism; today's in-repo skills keep working.

## Code pointers

- `cmd/arizuko/packages.go` — install/list/remove CLI
- `container/runner.go:seedSkills()` — already copies skills at spawn
- `ipc/connector.go` — existing MCP subprocess model (spec 5/13)
