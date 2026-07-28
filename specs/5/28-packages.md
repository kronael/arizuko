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

## Install lifecycle — a receipt, not a reconciler

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
REST APIs, recorded in a per-install receipt.** No universal reconciler.

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

### The install receipt (this is the provenance)

Each install writes an immutable receipt: package identity (source revision +
content hash), the file hashes it wrote, the resource primary keys it created,
and the last-applied payload hash per resource. **The receipt is the ownership
record the reconciler pretended to derive.**

- **remove** consults the receipt: delete only assets whose current hash still
  matches what the receipt recorded; on drift, **stop and report the exact
  conflict** (never silently overwrite or orphan). Refuse if another package's
  receipt depends on this one.
- **upgrade** (v1→v2) diffs the new plan against the receipt: three-way (base =
  last-applied, ours = live, theirs = new) so an operator's field edit and a
  package's field change don't clobber each other.

### Resolves `5/27` C2 correctly

`packages remove slakd` reads slakd's receipt, finds the `proxyd_routes` PK it
created with payload-hash H, and `DELETE /v1/routes/<pk>` **iff** the row still
hashes H. Operator-edited → drift-stop. The route lifecycle is owned by the
receipt + the proxyd REST handler, not a generic engine guessing ownership.

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
