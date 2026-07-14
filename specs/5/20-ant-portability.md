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
2. **Products — uv-style composable mixins.** The product is the one
   hostable, shareable, syncable unit; a group blends a LIST of them
   (manifest + lockfile + sources, immutable installs, clean-replace
   updates, migrations inside). For distribution and updates. No state
   inside.

They meet only at one point: a backup naturally contains the manifest +
lockfile, so a restored agent is reproducible — `product sync`
reinstalls byte-identical products from the lock.

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

## Mechanism 2 — products as composable mixins (uv-style)

One concept: **a product is the hostable, shareable, syncable unit; a
group is a blend of mixed-in products** plus its own state. A product
carrying only skills is a capability mixin; one carrying persona + facts
is an identity mixin; most carry a bit of each. `PRODUCT.md` (shipped)
stays the product's own manifest. Anyone hosts a product on their
GitHub; `ant/examples/<name>` are simply the bundled ones.

Per group:

- **Manifest — `~/products.toml`**: the mix, ordered.

  ```toml
  [[product]]
  source = "git+https://github.com/arizuko/products#support"   # identity mixin

  [[product]]
  source = "git+https://github.com/acme/aws-tools"             # capability mixin

  [[product]]
  source = "file:///opt/local/tufte"
  ```

- **Lock — `~/products.lock`**: per product the resolved immutable rev +
  sha256 tree hash + last-applied migration number. `sync` installs
  exactly this; the hash is the trust anchor, the URI a hint.

**Blend rules — how products combine, per payload kind:**

| Payload                      | Blend                                                         | On upstream update                                                   |
| ---------------------------- | ------------------------------------------------------------- | -------------------------------------------------------------------- |
| `skills/`                    | union by skill name; collision = refuse (rename in your fork) | **managed**: clean replace at new rev (immutable installs, no merge) |
| `PERSONA.md`                 | at most ONE product in the mix provides it; second = refuse   | seed-once: group state after create, never touched                   |
| `CLAUDE.md`                  | appended as marked sections, manifest order                   | seed-once                                                            |
| `facts/`, `tasks.toml`       | union; filename collision = refuse                            | seed-once                                                            |
| `settings.json` `mcpServers` | map union; name collision = refuse                            | managed (entry replaced)                                             |
| `[[env]]` checklist          | union                                                         | informational                                                        |
| `Dockerfile.ant` (Tier C)    | at most one in the mix                                        | operator rebuilds explicitly                                         |
| `migrations/NNN-*.md`        | per product                                                   | executed above the lock's mark on update                             |

The seed/managed distinction is per KIND, inside the product: skills and
mcpServers entries stay upstream-managed (that's what `sync`/`update`
touch); identity and knowledge seed once and become the group's own
state. Customization: overlay (`~/CLAUDE.md`, `.disabled`, custom
skills) or fork-and-repoint — a forked product stops tracking upstream,
deliberately. Never edit managed files in place; there is no merge and
therefore no merge state (what makes the lockfile legitimate).

- **Verbs**: `arizuko product add <inst> <folder> <source>`,
  `product update [<name>] [--to <ref>] [--dry-run]` (rollback = older
  `--to`), `product sync`, `product list`. Host CLI fetches; containers
  need no egress for product management. `arizuko create --product X`
  = create the group with `products.toml` listing X.

**Extension surface — what a product/package may carry, tiered by
trust.** Products carry much more than skills; each layer has an owner
and a boundary:

| Tier                    | What                                                                                                                                                                                                                                                                                                      | When / who                                    |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| **A — template seed**   | PERSONA.md, CLAUDE.md, `facts/`, `tasks.toml`, `[[env]]` checklist, **`[[mcp_server]]` entries** (written into the group's `settings.json` `mcpServers` at create — `seedSettings` preserves them; servers run in-container, egress-bounded), group-scoped seed rows (own-folder routes, scheduled tasks) | Once, at `create --product`; then group state |
| **B — skill package**   | SKILL.md + scripts — **including bundled binaries** (they execute in-container under the same grants/egress/mounts as any agent action; prefer runtime fetch via `uvx`/`bun` for arch-independence), own `migrations/`                                                                                    | Updatable via lock                            |
| **C — image extension** | a `Dockerfile.ant` (`FROM arizuko-ant` + system deps, e.g. aws-cli) the product declares; the operator **explicitly builds** it and the group runs it via a per-group `container_config.Image` override (NEW small mechanism — GroupConfig carries only `Mounts` today, `core/types.go:59`)               | Operator-built, never automatic               |
| **Never**               | daemons, cross-folder grants, host `connectors.toml`, platform settings keys (`seedSettings` rewrites those every spawn, `container/runner.go:850`)                                                                                                                                                       | —                                             |

The safety shape of Tier C matters: a custom image changes what software
exists INSIDE the sandbox — mounts, egress, and grants are set by the
platform per spawn regardless of image, so isolation properties are
image-independent. The trust decision is supply-chain (what you build),
made once, explicitly, by the operator. Agent-space stays the only
package-reachable surface; the platform is never package-modifiable.

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
