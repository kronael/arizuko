## status: decided

# Versioning & Persona Plugins

Agent versioning, skill distribution, and persona config.

## Current State

### Agent versioning

- `MIGRATION_VERSION` integer per group, compared to
  canonical version baked into agent image
- Gateway annotates container input when behind: "run `/migrate`"
- Root runs `/migrate` across all groups in `~/groups/*/`
- Migrations are numbered `.md` files with bash steps

### Image distribution

- Single `arizuko-agent:latest` built from `container/`
- Per-instance tags: `arizuko-agent-<name>:latest`
- `CONTAINER_IMAGE` in `.env` selects the tag
- Selective upgrades: tag + restart one instance

### Persona files

- `container/CLAUDE.md` seeded once to `~/.claude/CLAUDE.md`
- `container/skills/` seeded once to `~/.claude/skills/`
- Group folder: `SOUL.md`, `CLAUDE.md`, `facts/`
- Tier 2/3: RO mounts over inherited files

## Versioning: global integer (decided)

**Keep global `MIGRATION_VERSION` integer.** Per-skill
versioning adds complexity without solving a real problem
yet. The global integer works for linear migrations. When
it breaks (instance-specific paths), address then.

Rationale:

- Global integer is not broken, just inelegant
- Per-skill means reading YAML from every skill on spawn
- Transition from global to per-skill would break groups
- Instance-specific migrations: use conditional steps in
  migration .md files (check env/instance name, skip if
  not applicable)

Version lives in `MIGRATION_VERSION` file, not SKILL.md
frontmatter. One file, one integer, one comparison.

## Persona Plugins (decided)

A persona plugin is a composable unit of agent behavior:

```
plugin-support/
  PLUGIN.md          # name, description, version
  CLAUDE.md          # instructions (appended to agent)
  SOUL.md            # persona (optional, last wins)
  skills/            # skills to install
  facts/             # seed facts
  tasks.toml         # scheduled tasks
```

### Decided answers

- **Where do plugins live?** In the repo at `container/plugins/`.
  Instance data dir can override with `plugins/` directory.
- **Merge semantics**: CLAUDE.md sections appended in plugin
  order. Conflicts = last plugin wins. Keep it simple.
- **SOUL.md ownership**: one persona per group. Last plugin
  with SOUL.md wins. Explicit — declare in group config which
  plugin owns persona.
- **Skill name collisions**: error at spawn time. Two plugins
  cannot provide same skill name.
- **Plugin versioning**: integer, like migrations. Plugin
  carries `version: N` in PLUGIN.md frontmatter.
- **Runtime vs build-time**: mount at container spawn. Plugins
  are directories on the host, bind-mounted into container.
  No rebuild needed. Container runner reads group config,
  mounts listed plugin dirs.
- **Plugin dependencies**: deferred. Document in PLUGIN.md,
  human ensures both present. No resolver in v1.

## Minimal Next Step

Before full plugin system:

1. **Skill selection per group** — container runner reads
   `skills` list from group config, seeds only listed skills
2. **Scoped migration** — tier 1 (world admin) can run
   `/migrate` for own world, not just root

No plugin format or registry needed — just selective seeding
and scoped migration.

## World Templates

Product configs (e.g. code-researcher) as composable units:

```
container/worlds/code-researcher/
  SYSTEM.md           # system prompt
  SOUL.md.template    # persona with {NAME} placeholders
  facts/              # seed facts
  skills.txt          # skills to enable
  env.example         # required env vars
```

`arizuko create <name> --world code-researcher` scaffolds
from template. Future direction — manual setup for now.
