---
status: partial
---

# specs/5/28 — arizuko packages

> **Implementation status (2026-07-29).** Core lifecycle SHIPPED for the
> compose-fragment asset kind, record-backed + dirty-safe:
>
> - **P0** installed-package record store — `routd/packages_store.go`, migration
>   `0020`, `installed_packages` table + CRUD. (`7f41a63d`)
> - **P1** `arizuko packages install <source>` + record-driven `remove` —
>   `cmd/arizuko/packages.go`. (`c53ab0d2`)
> - **P3** `arizuko packages upgrade` with **dirty-detection** — refuses to
>   overwrite a locally edited asset (hash ≠ recorded). (`c0557df8`)
>
> Remaining: **P1b** git source resolution (install takes a local dir today),
> **P2** row assets (`proxyd_routes`/`acl` via owner REST — the live C2 fix),
> **P4** sidecar health-gating. Plan: `.ship/plan-packages.md`.

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

| Concern                                                                           | Spec                     |
| --------------------------------------------------------------------------------- | ------------------------ |
| **Package manager** — install / upgrade / remove one unit's lifecycle             | **this spec (5/28)**     |
| Producer side — what a product/package _contains_ (`PRODUCT.md`, persona, skills) | `5/21`                   |
| Composition — one group blends an ordered LIST of products (per-kind precedence)  | `5/20`                   |
| State transport — moving a live agent's rows/files (`export`/`apply`)             | `5/20` (mech. 1) + `5/8` |
| Compose fragment — one asset _kind_ (a file)                                      | `5/27`                   |
| Prototype — a product instantiated at spawn (`5/29` Tier-3)                       | `5/29`                   |
| OCI distribution envelope — rejected alternative                                  | `5/30` (superseded)      |

**5/28 is one package's lifecycle; 5/20 is state + how several products compose.**
They meet only at restore (below).

## Package = source + manifest + asset kinds

One manifest, and it is the **shipped `PRODUCT.md`** (`5/21`) — NOT a second
`package.yaml`. A package declares any SUBSET of asset kinds (no
`skill|agent|sidecar` type enum — presence of an asset is the behaviour):

| Asset kind       | What it is                                            | Owner handler                         |
| ---------------- | ----------------------------------------------------- | ------------------------------------- |
| skills           | `SKILL.md` dirs → `ant/skills/<name>/`                | `seedSkills` + `/migrate` 3-way merge |
| compose fragment | `services/<name>.yml` (+ `<name>-routes.json`)        | `5/27` (a file)                       |
| proxyd route     | a `proxyd_routes` row, keyed by path                  | `/v1/proxyd_routes`                   |
| grant            | an `acl` row, keyed by key                            | `/v1/acl`                             |
| group seed       | a folder template, **seeded once** → then local state | `SetupGroup`                          |

`requires:` (env vars) is a preflight **warning**, never a gate.

## The installed-package record (the one new mechanism)

`5/20`'s `products.lock` generalises to a single per-instance **installed
record**, one entry per installed package:

- **source** + resolved **immutable revision**;
- the **manifest as installed** — the exact identities this install owns (route
  paths, acl keys, skill dirs, files, fragment names);
- a per-asset **content hash** (what was written).

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

## Resolves `5/27` C2

`packages remove slakd` reads the installed record, finds it owns the
`proxyd_routes` row for `/slack/`, and `DELETE /v1/proxyd_routes/<path>` through
proxyd's handler — the live table updates (not a JSON blob proxyd ignores when
its table is non-empty). The record names what to remove; no ownership guessing.

## Composition + restore ordering (both → `5/20`)

- **Two packages in one group:** deterministic per-kind precedence is `5/20`'s
  blend rule (persona: first-wins; skills: last-wins wholesale; CLAUDE.md:
  appended; rows: union, cross-package collision refused). That IS the collision
  rule — 5/28 owns one package; 5/20 owns how several compose.
- **Restore vs install:** both write rows. Rule — **restore agent state first,
  then package sync reasserts package-declared identities**, so a cloned agent's
  recorded packages re-install their rows over the restored baseline.

## Reconciler alternative — demolished

> A declarative reconciler (apply/export/remove as one engine over resreg rows,
> `provenance = ownership`) was drafted here and demolished by codex
> (2026-07-28, `.ship/critique-cto-20260728.md`, 17 findings / 9 fatal). Chief
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

## Code pointers

- `cmd/arizuko/packages.go` — install/list/remove CLI (today: fragment copy only)
- `container/runner.go:seedSkills()` — skill seeding at spawn
- `resreg/resources/proxyd_routes.go`, `routd/acl_resource.go` — the owner handlers
- `ant/skills/self/migration.md` — the 3-way merge packages reuse for skills
