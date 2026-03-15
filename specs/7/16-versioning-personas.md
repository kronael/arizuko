## <!-- source: arizuko specs/4/K-versioning-personas.md, synced 2026-03-15 -->

## status: open

# Versioning & Persona Plugins

How agent versioning, skill distribution, and persona configuration
should work at scale. Currently ad-hoc — needs a coherent model.

## Current State

### Agent versioning

- `MIGRATION_VERSION` integer per group, compared to canonical version
  baked into agent image
- Gateway annotates container input when behind: "run `/migrate`"
- Root runs `/migrate` which copies skills + runs migration scripts
  across all groups in `~/groups/*/`
- Migrations are numbered `.md` files with bash steps

### Image distribution

- Single `arizuko-agent:latest` built from `container/`
- Per-instance tags: `arizuko-agent-<name>:latest`
- `CONTAINER_IMAGE` in each instance's `.env` selects the tag
- Selective upgrades: tag + restart one instance at a time

### Persona files

- `container/CLAUDE.md` → seeded once to `~/.claude/CLAUDE.md`
- `container/skills/` → seeded once to `~/.claude/skills/`
- Group folder: `SOUL.md`, `CLAUDE.md` (group-level), `facts/`
- Tier 2/3: RO mounts over inherited files

## Problems

1. **All instances share one agent image** — every persona gets every
   skill, every migration, every tool. A support bot carries research
   skills it never uses.

2. **Persona = manual file editing** — creating a new persona means
   manually writing CLAUDE.md, SOUL.md, placing facts, configuring
   `.env`. No composable units.

3. **Skills are all-or-nothing** — baked into image, seeded on first
   spawn, then the agent owns them. No way to add/remove skills per
   group without manual intervention.

4. **Migration is root-only** — `/migrate` runs from tier 0, iterates
   all groups. No self-service migration for individual worlds.

5. **No version pinning** — can't say "this group runs skill version X
   while that group runs version Y". All groups converge to latest.

## Persona Plugins

A persona plugin is a composable unit of agent behavior:

```
plugin-support/
  PLUGIN.md          # metadata: name, description, dependencies
  CLAUDE.md          # instructions (merged into agent CLAUDE.md)
  SOUL.md            # persona (optional, overrides default)
  skills/            # skills to install
  facts/             # seed facts
  tasks.toml         # scheduled tasks
```

### Composition

A group's effective behavior = base image + plugins applied in order.

```toml
# group config (in .env or groups.toml or similar)
[group.atlas/support]
plugins = ["base-chat", "support", "facts-verifier"]
```

### Open Questions

- **Where do plugins live?** In the repo? In the instance data dir?
  In a separate registry? `container/plugins/`?
- **Merge semantics for CLAUDE.md** — append? Section-based merge?
  What if two plugins conflict?
- **SOUL.md ownership** — only one persona per group. Last plugin
  wins? Explicit override? Error on conflict?
- **Skill name collisions** — two plugins provide `/research`?
- **Plugin versioning** — semver? Integer like migrations? Git refs?
- **Runtime vs build-time** — bake plugins into image, or mount at
  container spawn? Baking is simpler but requires rebuild.
  Mounting is flexible but adds complexity to container-runner.
- **Plugin dependencies** — `support` requires `facts-verifier`.
  Declare in PLUGIN.md? Resolve at spawn time?

## Versioning Model

### Current: single integer

Works for linear migrations. Breaks when:

- Different instances need different migration paths
- A migration is instance-specific (REDACTED vs REDACTED)
- Skills evolve independently of migrations

### Proposed: per-skill versioning

Each skill carries its own version:

```yaml
# skills/facts/SKILL.md frontmatter
name: facts
version: 3
```

Migration runner checks per-skill versions, not one global int.
Skills can be updated independently.

### Open Questions

- **Per-skill vs global** — per-skill is more granular but adds
  complexity. Is the current global integer actually broken, or
  just inelegant?
- **Version in SKILL.md vs separate file** — frontmatter is cleaner
  but means reading YAML from every skill on every spawn.
- **Backwards compatibility** — how to transition from global int
  to per-skill without breaking existing groups?
- **Instance-specific migrations** — some migrations only apply to
  REDACTED (e.g., support agent changes). Skip mechanism? Conditional?

## Instance Repos (related: 4/G)

`4/G-instance-repos.md` proposes git repos per instance. Persona
plugins could be the unit of composition within those repos:

```
REDACTED/
  .env.example
  plugins/
    base-chat/
    support/
    facts-verifier/
  groups/
    root/
    atlas/
      plugins = ["base-chat"]
    atlas/support/
      plugins = ["base-chat", "support", "facts-verifier"]
```

## Minimal Next Step

Before building the full plugin system, the smallest useful change:

1. **Skill selection per group** — `container-runner.ts` reads a
   `skills` list from group config, only seeds listed skills
   instead of all `container/skills/*`
2. **Group-level migration awareness** — world admins (tier 1) can
   run `/migrate` for their own world, not just root

These don't require a plugin format or registry — just selective
seeding and scoped migration.

## Shipped Worlds

Product configs like the code-researcher (`3/3-code-research.md`) are the
natural realization of persona plugins — complete product configs packaged
as a unit: SYSTEM.md, SOUL.md, CLAUDE.md overrides, skills, seed facts.

These could ship as **world templates** via `container/worlds/`:

```
container/worlds/code-researcher/
  SYSTEM.md         # research-focused system prompt
  SOUL.md.template  # persona skeleton with {NAME} placeholders
  facts/            # seed facts (empty or starter set)
  skills.txt        # list of skills to enable
  env.example       # required env vars (EXTRA_MOUNTS, etc.)
```

`arizuko create <name> --world code-researcher` would scaffold a new
instance from the template. Migrations can update world templates.
Users fork or extend by editing the group folder directly.

This is future direction — the code-researcher currently deploys via
manual setup (see the spec's howto cookbook). World templates formalize
the pattern once more product configs exist.

## Related

- `4/G-instance-repos.md` — git-based instance config
- `3/5-permissions.md` — tier model, mount enforcement
- `1/B-extend-skills.md` — skill system, /migrate
- `1/X-sync.md` — migration system
- `3/3-code-research.md` — first shipped product config (concrete example)
