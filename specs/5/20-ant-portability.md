---
status: draft
moved-from: specs/17/7-ant-portability.md
rewritten: 2026-07-14
depends: [8-yaml-manifests]
---

# specs/5/20 — portable agents: export/apply, pg_dump-style

How an agent (folder + its arizuko rows) moves between instances and how
shared products are distributed. Rewritten 2026-07-14: a codex audit
(`.ship/codex-portability.log`) found the prior draft built parallel
machinery for four shipped contracts, and the user then set the model:
**behave like `pg_dump` — one export/apply pair; flags decide meta-only
vs with-data; the format follows the content.**

## The model

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

## Incremental updates — templates + skills from a source

Full import is not the only flow: an installed third-party template or
skill must be updatable in place. The shipped `/migrate` merge only
covers stock content (`/opt/arizuko/ant/`); source-installed content
would otherwise freeze at install (custom = never touched). The fix
reuses the same machinery — no lockfile, no hashes:

- **Provenance, not merge state.** `~/.claude/sources.toml` per group
  maps a path prefix to `{source, rev}` — e.g. `skills/kb-search` →
  `git+https://…@<sha>#kb-search`. Pure inventory; the merge base (the
  bytes) stays in `.claude/.merge-base/` exactly like stock content.
- **Install** (`arizuko skill add <inst> <folder> <source>`, or arriving
  via a product import): copy files, snapshot them into `.merge-base/`,
  record `{source, rev}`.
- **Update** (`arizuko skill update <inst> [--folder …] [--dry-run]`):
  for each sourced prefix, fetch source at its ref's current tip →
  `theirs`; `.merge-base/` → `base`; live file → `ours`; the identical
  3-way merge `/migrate` runs (operator edits survive; both-changed →
  conflict markers, surfaced). On success refresh `.merge-base/` + `rev`.
  Rollback is just `update` pinned to an older rev (`--to <ref>`).
- **Templates the same way**: a product's PERSONA.md/CLAUDE.md installed
  from a source get a `sources.toml` entry and update through the same
  path. Stock skills are untouched by this — they keep the
  `MIGRATION_VERSION` trigger; a prefix is owned by exactly one of
  {stock, sourced, local} and the two update paths never overlap.

No fleet add/remove/rollback/remap verb zoo beyond this pair; `--groups`
globbing can come later if operating many groups demands it.

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
