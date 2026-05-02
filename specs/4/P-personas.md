---
status: shipped
---

# Versioning & Personas

## Versioning

- `MIGRATION_VERSION` integer per group, baked into agent image
- **One migration file per release.** File name
  `NNN-vX.Y.Z-summary.md` ties the integer to the CHANGELOG version.
  Every release ships one — including docs-only — so the auto-migrate
  trigger fires on every version. Stub body for no-skill-change
  releases is fine ("release-marker for vX.Y.Z broadcast").
- Gateway compares per-root-group `MIGRATION_VERSION` against the
  source's on every gated start (`gateway.checkMigrationVersion`);
  behind → posts `/migrate` to the root agent + notifies child groups
- `/migrate` diffs canonical vs session skills, copies changed
  skill dirs, runs numbered `.md` migration scripts in order, then
  broadcasts the latest CHANGELOG blockquote to every group
- Version lives in `MIGRATION_VERSION` file, not skill frontmatter

Rationale: tying the broadcast to skill changes alone misses
docs-only releases (the v0.33.1 web-docs polish never reached chats
because nothing bumped the integer). Tying to the CHANGELOG via a
second trigger path duplicates state. The "one migration per
release" rule keeps the trigger in one place: skill update and
announce ride the same auto-migrate hook. Global integer is not
broken, just inelegant; per-skill counters would mean parsing YAML
from every skill on spawn. Instance-specific migrations: conditional
steps inside the migration `.md`.

## Image distribution

- Single `arizuko-ant:latest` built from `ant/`
- Per-instance tags: `arizuko-ant-<name>:latest`
- `CONTAINER_IMAGE` in `.env` selects the tag
- Selective upgrades: tag + restart one instance

## Persona files

- `ant/CLAUDE.md` seeded to `~/.claude/CLAUDE.md`
- `ant/skills/` seeded to `~/.claude/skills/`
- Group folder: `SOUL.md`, `CLAUDE.md`, `facts/`
- Tier 2/3: ro mounts over inherited files

Products (`R-products.md`) deliver curated persona + skill bundles
per group, selected at creation.
