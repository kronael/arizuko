---
status: draft
moved-from: specs/17/7-ant-portability.md
rewritten: 2026-07-14
depends: [8-yaml-manifests]
---

# specs/5/20 — portable agents: state transport + package manager

How an agent (folder + its arizuko rows) moves between instances and how
shared products/skills are distributed. Rewritten 2026-07-14 (codex audit
in `.ship/codex-portability.log`, then user direction): **two mechanisms,
deliberately, because state and software are different concerns.**

1. **State transport — pg_dump-style.** `arizuko export/apply`; flags
   decide meta-only vs with-data; format follows content. For backups
   and instance-to-instance moves. No source resolution, no merging.
2. **Packages — uv-style.** Products and skills are _packages_: a
   per-group manifest + lockfile + sources, immutable installs,
   clean-replace updates, migrations shipped inside the package. For
   distribution and updates. No state inside.

They meet only at one point: a backup naturally contains the manifest +
lockfile, so a restored agent is reproducible — `pkg sync` reinstalls
byte-identical packages from the lock.

## Mechanism 1 — state transport (export/apply)

`pg_dump` gives you the same tool for a schema-only dump and a full one;
the flag chooses. arizuko's twin:

- **Meta (config) = the 5/8 resreg YAML rows.** groups, acl, routes,
  web_routes, network_rules, scheduled_tasks; secrets as metadata only.
  Output: `agent.yml` (gzip optional). This is today's `arizuko export`,
  gaining folder scope.
- **Data = the agent's folder tree.** PERSONA.md, CLAUDE.md, `skills/`
  (whole directories — scripts, subdirs, binaries, verbatim, no
  inlining), `facts/`, `users/`, `diary/`, `workspace/`, `.claude/`
  incl. `.merge-base/` (so the shipped 3-way-merge state travels) and
  the operator's `settings.json` `mcpServers` entries (minus the
  platform `arizuko` one). Files don't belong inline in YAML — with
  `--files` the output becomes an archive: `agent.tar.gz` containing
  `config.yml` (the same rows + a `meta:` header) and `files/` (the
  tree, verbatim).

```
arizuko export <inst>                          # whole-DB config YAML (5/8, today)
arizuko export <inst> --folder acme/eng        # one subtree's rows → agent.yml
arizuko export <inst> --folder acme/eng --files -o eng.tar.gz   # rows + folder tree
arizuko apply  <inst> eng.tar.gz [--to <folder>] [--dry-run]
arizuko apply  <inst> agent.yml  [--dry-run]   # rows only (5/8, today)
```

No new verbs, no `pack` namespace, no `import`: `apply` detects the
format (`.yml` → rows; `.tar.gz` → rows + files). Applying files refuses
to overwrite an existing group unless `--to` names a fresh folder or
`--force` is explicit. `--dry-run` prints the row diff + file list.
tar.gz, not zip: arizuko is Linux-only and the instance-backup story is
already "one tar of the directory".

The `meta:` header in `config.yml` (arizuko version, exported_at, source
folder, resolved git revision when fetched) is the manifest. No
`manifest.json`, no bespoke envelope.

## Canonical owners (nothing here re-implements them)

| Concern                                  | Owner (shipped)                                                   |
| ---------------------------------------- | ----------------------------------------------------------------- |
| Merge state / operator-edit preservation | `.claude/.merge-base` + `/migrate` (ant/skills/self/migration.md) |
| Update trigger                           | `MIGRATION_VERSION` (routd/loop.go:425)                           |
| Product identity + env requirements      | `ant/examples/<name>/PRODUCT.md` (cmd/arizuko productManifest)    |
| Row serialization                        | resreg `Export`/`EmitYAML` (resreg/engine.go:624,837)             |
| Skill seeding on install                 | `SetupGroup` + `seedSkills` (container/runner.go:972,1024)        |

Upgrade after import is free: the imported `.merge-base` is the merge
base; when the target's `MIGRATION_VERSION` is ahead, routd enqueues
`/migrate` and the shipped 3-way merge brings stock files current while
preserving the imported edits. Custom skills (not under
`/opt/arizuko/ant/skills/`) travel verbatim and are never touched by
migration — sharing a hand-built skill is just exporting the folder.

## Excluded, reported on export (never silent)

- **Secret values** — never travel. Rows carry secrets _metadata_
  (scope, key); `apply` finishes by printing exact `arizuko secret set` /
  `user-secret set` skeletons for every missing key + PRODUCT.md
  `[[env]]` requirements. A target without `SECRETS_KEY` refuses
  (precondition; BUGS S2 — the plaintext fallback must fail loud first).
- **Web slots** — `public_html`/`private_html` live outside the group
  dir (container/runner.go:622); excluded, listed in the report.
- **Sessions** — deferred to v2. Transcripts without routd's
  `(folder, topic) → session_id` rows are orphans; a session payload
  needs both halves.
- **Media, logs, caches, sockets, the world share; `connectors.toml`**
  (instance-level host dependency — `apply` lists referenced connector
  names as unmet host requirements).

## Sources (product distribution)

v1 resolvers: local file/dir and `git+https://…@<ref>#<subdir>` resolved
to an immutable commit (recorded in `meta:`). A shared product is this
archive (or bare yml) fetched from a repo — publishable anywhere git
reaches. No registry, no `claude-plugin:` scheme until a stable plugin
API exists.

## Mechanism 2 — skill packages (uv-style) + products as templates

Naming, resolved: **a product is a template; a skill is a package**
("plugin" is the ecosystem word for the same thing). The distinction is
what updates:

- **Product = template, instantiated once.** Persona + seed facts +
  skill list + `[[env]]` checklist (`PRODUCT.md`, shipped). At
  `arizuko create --product <name-or-source>` its files are copied into
  the group (`SetupGroup`) and from that moment they are the group's own
  STATE — operator-owned (PERSONA.md is canonical operator truth), never
  updated from upstream. New template versions benefit new groups.
  Sources: bundled `ant/examples/<name>` or
  `git+https://…#<subdir>` — anyone hosts products on their GitHub.
- **Skill = package, managed and updatable.** The ONLY managed surface.
  Precedent is shipped: the platform's own migrations live inside a
  skill (`ant/skills/self/migrations/`).

Per group:

- **Manifest — `~/skills.toml`**: `[[skill]] name/source` entries,
  human- or `skill add`-written.
- **Lock — `~/skills.lock`**: resolved immutable rev + sha256 tree hash
  - last-applied migration number per skill. `sync` installs exactly
    this; the hash is the trust anchor, the URI a hint.
- **Immutable installs.** Never edited in place; update = clean replace
  at the new rev. No merge, hence no merge state — this is what makes
  the lockfile legitimate where the old draft's failed. Customization
  lives in the overlay arizuko already has: `~/CLAUDE.md`, `PERSONA.md`,
  custom skills, `.disabled`. Deep changes = fork and repoint the
  manifest; a forked skill stops tracking upstream, deliberately.
- **Migrations ride the package.** `migrations/NNN-*.md`, the platform's
  own convention; `skill update` replaces the tree, then enqueues a turn
  where the agent executes files above the lock's mark, in order, and
  the lock records the new mark. A skill may seed/transform ITS OWN data
  this way (e.g. create `~/facts/<x>.md` if missing); identity files
  stay template-seeded — deterministic copy beats agent-executed
  seeding for persona-class files.
- **Verbs**: `arizuko skill add <inst> <folder> <source>`,
  `skill update [<name>] [--to <ref>] [--dry-run]` (rollback = older
  `--to`), `skill sync`, `skill list`. Host CLI fetches; containers need
  no egress for package management.

**Extension surface — how much a package may modify arizuko: agent-space
only.** A skill adds prompt capability + scripts that run inside the
container as the agent, bounded by the same grants, egress allowlist,
and mounts as any agent action. It may self-register an in-container MCP
server (the shipped `loadAgentMcpServers` path, `ant/src/mcp-servers.ts`).
It can NOT touch daemons, routes, grants, host `connectors.toml`, or
platform settings keys — `seedSettings` rewrites those authoritatively
every spawn (`container/runner.go:850`), so the boundary is enforced by
construction. The platform is not package-modifiable; wiring stays
operator-only.

**Boundary with the shipped stock mechanism**: stock skills
(`/opt/arizuko/ant/`) keep `MIGRATION_VERSION` + `.merge-base/` — the
ONLY place 3-way merging exists. A path is owned by exactly one of
{stock, package, local}; the mechanisms never overlap on a file.
(Later, stock could itself become the platform package — a possibility,
not v1.)

**Merge moves to the harness (user-directed 2026-07-14, changes the
shipped path).** Today the `/migrate` skill performs the whole 3-way
walk agent-side. That inverts: the **harness** does the mechanical merge
in deterministic code — a small, isolated Go lib run at seed/spawn time
(where `seedSkills` already walks these files with `.merge-base` at
hand): new-upstream → copy, only-ours → keep, both-changed → write
conflict markers + record the file. The agent's `/migrate` shrinks to
**conflict resolution only** — a turn triggered when conflicts exist,
presented the marked files. Judgment stays with the agent; mechanics
leave it. Constraint: the merge lib stays minimal and isolated (no
growth into a package manager — packaging is the CLI's job above), so
code owns the deterministic 90% without blowing up. Migration
instruction files (`migrations/NNN-*.md`) remain agent-executed — they
are instructions by design.

## Blockers (tracked in BUGS.md)

- 5/8 CLI still opens frozen `messages.db` — owner-DB repoint required.
- `resreg.Export` emits raw invite bearer tokens — token-resource
  exclusion required before any export ships.
- `SECRETS_KEY` warn-and-continue plaintext fallback (S2, HIGH) — must
  fail loud so `apply`'s precondition is trustworthy.

## Deleted from the prior draft

`.ant.lock.json` + hash-merge + layer ownership; `ant.toml` +
`ant materialize`; per-skill migration scripts + `applied_migrations`;
`MCP.json`; `channels.json`/`schedule.json`/`grants.json`/`store.jsonl`;
the `.arzpack` format + `manifest.json`; a separate import verb; fleet
add/update/remove/rollback/remap verbs; LLM-merge; content-hash-as-trust.

## Acceptance

- `export --folder --files` → `apply --to` on a second instance
  round-trips a non-trivial group (≥5 skills incl. one custom with
  scripts, facts, diary, an mcpServers entry); re-export matches modulo
  `meta:`.
- Imported group with an older merge-base self-migrates on the target's
  next `MIGRATION_VERSION` bump without losing imported operator edits;
  the custom skill is untouched.
- No secret value, invite token, or route token appears in any export
  (content-level test).
- `apply` on a target without `SECRETS_KEY` refuses with the exact fix.
- Export report names every excluded category present in the source.
- Plain `arizuko export <inst>` output is byte-identical to today's
  (5/8 unchanged when no new flag is passed).
