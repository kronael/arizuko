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

## Declarative apply / reconcile model

The install-flow steps above are imperative shorthand; the real contract is a
**reconciler over resreg resources**. `apply`, `export`, and `remove` are one
engine run in three directions — never a per-package script.

- **apply** = manifest → state. Diff desired vs actual; create missing, update
  drifted, delete extra. Idempotent: re-apply is a no-op.
- **export** = state → manifest. Walk the resreg resources a world/agent owns,
  serialize their rows back into `arizuko.yaml` + the folder files. One generic
  exporter over the resreg registry — a new resource is exportable for free
  (`5/17` one-handler discipline). This is `5/20` portability.
- **remove** = apply a negated manifest. The reconciler deletes the rows it
  owns. No separate uninstall path.

### `arizuko.yaml` — the apply manifest

A fixed vocabulary of typed verbs, each targeting one resreg resource. NOT a
script — data the reconciler diffs:

```yaml
apply:
  - route.add: { path: /slack/, backend: http://slakd:8080, auth: public }
    when: SLACK_BOT_TOKEN # declared predicate — skip if unset
  - grant.add: { subject: '@ops', action: 'reply', target: 'eng/*' }
  - seed_group: { folder: sloth, prototype: ./prototype/ }
  - env.set: { TTS_ENABLED: 'true' }
```

`when:` is the only conditional — a predicate on env/state, evaluated by the
engine (generalises today's `gated_by`). There is no `if`/loop/expression.

### The "ifs"

Three tiers, none imperative in the package:

1. **Idempotent reconcile** absorbs `if-not-exists`/`if-changed` — the engine's
   diff, not the package's code.
2. **`when:` predicates** express real conditions as data.
3. **Genuine branching logic** (compute-a-decision) escalates to an **agent
   session**: the package ships a setup **prototype** (`5/29` Tier-3), the agent
   runs it with MCP tools — sandboxed (crackbox), grant-gated, audited. Declarative
   covers the mechanical majority; the agent covers the conditional remainder.

### Resolves `5/27` C2 (package-route lifecycle)

Because `remove` reconciles through the engine that **owns** `proxyd_routes`,
`packages remove slakd` deletes the route row — not just the regenerated
`PROXYD_ROUTES_JSON` blob proxyd ignores when its table is non-empty. Provenance
is the reconciler's ownership: a package-installed row is the package's to
delete; an operator-edited row diffs as drift and is left alone. No ownership
drift, no dangling route.

## Discovery and interconnection

**Discovery**: GitHub topic `arizuko-package`. No registry needed.

**Shared libraries**: existing `share/` mount (`/var/lib/share`
→ `groups/<world>/share/`) is the mechanism. Packages can seed files there.

**Interconnection**: via skills (agent invokes by SKILL.md name) and via
MCP (sidecar provides tool endpoints). No new protocol — MCP is the interface.

### Out of scope v1

Multi-env support (skill with `requires: {dev: [VAR], prod: [OTHER_VAR]}`).
Package registry or central catalog (GitHub topic search is discovery).
Package dependencies (skill A requires skill B — just document in README).

## What deletes

Nothing. This adds a distribution mechanism; today's in-repo skills keep working.

## Code pointers

- `cmd/arizuko/packages.go` — install/list/remove CLI
- `container/runner.go:seedSkills()` — already copies skills at spawn
- `ipc/connector.go` — existing MCP subprocess model (spec 5/13)
