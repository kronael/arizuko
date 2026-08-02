---
status: draft
moved-from: specs/17/7-ant-portability.md
rewritten: 2026-07-14
depends: [8-yaml-manifests]
---

# specs/5/20 — portable agents: state transport + product composition

> **DECISION: two mechanisms, deliberately, because state and software are
> different concerns.**
>
> 1. **State transport — pg_dump-style.** `arizuko export`/`apply`; flags
>    decide meta-only vs with-data. For backups and instance-to-instance
>    moves. No source resolution, no merging.
> 2. **Products — uv-style composable mixins.** The product is the
>    hostable, shareable, syncable unit; a group blends a LIST of them. For
>    distribution and updates. No state inside.
>
> They meet at exactly one point: a backup contains the manifest + lock, so
> a restored agent is reproducible.

**Scope after `5/28` (2026-07-29).** One package's install/upgrade/remove
lifecycle is canonical in [`5/28`](28-packages.md); `products.lock` IS
`5/28`'s installed-package record (one mechanism, not two). This spec keeps
**state transport** and **composition** — how a group blends an ordered
LIST of products, which is the collision rule `5/28` defers here. On
restore/clone the ordering is: apply agent state first, THEN package sync
reasserts package-declared rows.

## Mechanism 1 — state transport

`pg_dump` gives one tool for a schema-only dump and a full one; the flag
chooses. arizuko's twin:

- **Meta (config) = the `5/8` resreg YAML rows** — groups, acl, routes,
  web_routes, network_rules, scheduled_tasks; secrets as metadata only.
- **Data = the agent's folder tree** — `PERSONA.md`, `CLAUDE.md`,
  `skills/` (whole directories, verbatim, no inlining), `facts/`, `users/`,
  `diary/`, `workspace/`, `.claude/` including `.merge-base/` (so the
  3-way-merge state travels), and the operator's `settings.json`
  `mcpServers` entries minus the platform `arizuko` one.

**Files don't belong inline in YAML**, so `--files` makes the output an
archive (`agent.tar.gz`: `config.yml` + `files/`). tar.gz not zip —
arizuko is Linux-only and the backup story is already "one tar".

```
arizuko export <inst> [--folder acme/eng] [--files -o eng.tar.gz]
arizuko apply  <inst> {eng.tar.gz | agent.yml} [--to <folder>] [--dry-run]
```

**No new verbs, no `pack` namespace, no `import`:** `apply` detects the
format (`.yml` → rows; `.tar.gz` → rows + files). Applying files refuses to
overwrite an existing group unless `--to` names a fresh folder or `--force`
is explicit. The `meta:` header in `config.yml` (version, exported_at,
source folder, resolved git revision) IS the manifest — no `manifest.json`,
no bespoke envelope.

Upgrade after import is free: the imported `.merge-base` is the merge base,
so when the target's `MIGRATION_VERSION` is ahead, routd enqueues
`/migrate` and the 3-way merge brings stock files current while preserving
imported edits. Custom skills (outside `/opt/arizuko/ant/skills/`) travel
verbatim and are never touched by migration — sharing a hand-built skill is
just exporting the folder.

### Excluded, reported on export (never silent)

- **Secret values.** Rows carry metadata only; `apply` finishes by printing
  exact `arizuko secret set` skeletons for every missing key. A target
  without `SECRETS_KEY` refuses.
- **Web slots** — `public_html`/`private_html` live outside the group dir.
- **Sessions** — deferred to v2. Transcripts without routd's
  `(folder, topic) → session_id` rows are orphans; a session payload needs
  both halves.
- **Media, logs, caches, sockets, the world share, `connectors.toml`** (an
  instance-level host dependency — `apply` lists referenced connector names
  as unmet host requirements).

### Sources

v1 resolvers: local file/dir, and `git+https://…@<ref>#<subdir>` resolved
to an immutable commit recorded in `meta:`. A shared product is that
archive fetched from a repo — publishable anywhere git reaches. No
registry, and no `claude-plugin:` scheme until a stable plugin API exists.

## Mechanism 2 — products as composable mixins

**A product is the hostable unit; a group is a blend of mixed-in products**
plus its own state. A product carrying only skills is a capability mixin;
one carrying persona + facts is an identity mixin. `PRODUCT.md` stays the
product's own manifest — producer side in [`21-products.md`](21-products.md).
Anyone hosts a product on their GitHub; `ant/examples/<name>` are simply
the bundled ones.

Per group: `~/products.toml` is the ordered mix (each entry a `source =`);
`~/products.lock` records per product the resolved immutable rev + sha256
tree hash + last-applied migration number. **The hash is the trust anchor,
the URI a hint.**

### Blend rules — how products combine, per payload kind

| Payload                      | Blend                                      | On upstream update        |
| ---------------------------- | ------------------------------------------ | ------------------------- |
| `skills/`                    | union by name; LAST product wins wholesale | managed: clean replace    |
| `PERSONA.md`                 | FIRST provider wins; later warned          | seed-once                 |
| `CLAUDE.md`                  | appended as marked sections, in order      | seed-once                 |
| `facts/`, `tasks.toml`       | union; filename collision = refuse         | seed-once                 |
| `settings.json` `mcpServers` | map union; name collision = refuse         | managed                   |
| `Dockerfile.ant` (Tier C)    | at most one in the mix                     | operator rebuilds         |
| `migrations/NNN-*.md`        | per product                                | run above the lock's mark |

**Skills collide by override, not merge**, because two providers share no
merge base — a git-style merge is undefined there, so wholesale override is
the only safe blend.

**The seed/managed distinction is per KIND, inside the product.** Skills
and mcpServers entries stay upstream-managed (what `sync`/`update` touch);
identity and knowledge seed once and become the group's own state.
Customization is overlay (`~/CLAUDE.md`, `.disabled`, custom skills) or
fork-and-repoint — a fork stops tracking upstream, deliberately. **Never
edit managed files in place**; there is no merge and therefore no merge
state, which is what makes the lockfile legitimate.

Verbs: `arizuko product add|update|sync|list` (`update --to <ref>` doubles
as rollback). The host CLI fetches, so containers need no egress for
product management.

### Extension surface — tiered by trust

| Tier                    | What                                                                                                                                   | When / who                       |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------- |
| **A — template seed**   | PERSONA.md, CLAUDE.md, `facts/`, `tasks.toml`, `[[env]]` checklist, `[[mcp_server]]` entries, group-scoped seed rows                   | once at create; then group state |
| **B — skill package**   | SKILL.md + scripts, including bundled binaries, own `migrations/`                                                                      | updatable via lock               |
| **C — image extension** | a `Dockerfile.ant` (`FROM arizuko-ant` + system deps) the operator explicitly builds; the group runs it via a per-group image override | operator-built, never automatic  |
| **Never**               | daemons, cross-folder grants, host `connectors.toml`, platform settings keys                                                           | —                                |

**Why Tier C is safe to allow at all:** a custom image changes what
software exists INSIDE the sandbox, but mounts, egress, and grants are set
by the platform per spawn regardless of image — isolation properties are
image-independent. The trust decision is supply-chain, made once,
explicitly, by the operator. Agent-space stays the only package-reachable
surface; the platform is never package-modifiable.

**A path is owned by exactly one of {stock, package, local}** — the
mechanisms never overlap on a file. Stock skills keep `MIGRATION_VERSION` +
`.merge-base/`, the ONLY place 3-way merging exists.

### Merge moves to the harness

**User-directed 2026-07-14; changes the shipped path.** Today `/migrate`
performs the whole 3-way walk agent-side. That inverts: the **harness**
does the mechanical merge in deterministic Go at seed/spawn time (where
`seedSkills` already walks these files with `.merge-base` at hand) —
new-upstream → copy, only-ours → keep, both-changed → conflict markers +
record the file. `/migrate` shrinks to **conflict resolution only**, a turn
triggered when conflicts exist. Judgment stays with the agent; mechanics
leave it.

Constraint: the merge lib stays minimal and isolated — no growth into a
package manager, since packaging is the CLI's job. Migration instruction
files (`migrations/NNN-*.md`) remain agent-executed; they are instructions
by design.

## Canonical owners (nothing here re-implements them)

| Concern                                  | Owner (shipped)                                                  |
| ---------------------------------------- | ---------------------------------------------------------------- |
| Merge state / operator-edit preservation | `.claude/.merge-base` + `ant/skills/self/migration.md`           |
| Update trigger                           | `MIGRATION_VERSION` (`routd/loop.go:434`)                        |
| Product identity + env requirements      | `PRODUCT.md` (`cmd/arizuko/main.go:28` productManifest)          |
| Row serialization                        | `resreg.Export` (`resreg/engine.go:627`), `EmitYAML` (`:840`)    |
| Skill seeding on install                 | `SetupGroup` (`container/runner.go:965`), `seedSkills` (`:1017`) |

## Blockers (tracked in BUGS.md)

- `5/8`'s CLI still opens the frozen `messages.db` — owner-DB repoint
  required before any of this reaches a production instance.
- `resreg.Export` emits raw invite bearer tokens: `invites` is registered
  with a `RowType` and no `SkipApplyRebuild`
  (`resreg/resources/invites.go:36`), unlike `route_tokens`. Token-resource
  exclusion is required before any export ships.
- `SECRETS_KEY` warn-and-continue plaintext fallback (S2, HIGH) must fail
  loud before `apply`'s precondition is trustworthy.

## Acceptance

- `export --folder --files` → `apply --to` on a second instance
  round-trips a non-trivial group (≥5 skills including one custom with
  scripts, facts, diary, an mcpServers entry); re-export matches modulo
  `meta:`.
- An imported group with an older merge-base self-migrates on the target's
  next `MIGRATION_VERSION` bump without losing imported edits; the custom
  skill is untouched.
- No secret value, invite token, or route token appears in any export
  (content-level test).
- `apply` on a target without `SECRETS_KEY` refuses with the exact fix.
- Plain `arizuko export <inst>` output stays byte-identical to today's.
