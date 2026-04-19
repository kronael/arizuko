---
status: partial
---

# Versioning & Personas

Global integer versioning shipped. Plugin composition deferred; see
`products.md` for the shipped alternative.

## Versioning (shipped)

- `MIGRATION_VERSION` integer per group, baked into agent image
- Gateway annotates container input when behind: "run `/migrate`"
- `/migrate` diffs canonical vs session skills, copies changed
  skill dirs, runs numbered `.md` migration scripts
- Version lives in `MIGRATION_VERSION` file, not skill frontmatter

Rationale: global integer is not broken, just inelegant. Per-skill
means reading YAML from every skill on spawn. Instance-specific
migrations: use conditional steps in migration `.md` files.

## Image distribution (shipped)

- Single `arizuko-agent:latest` built from `ant/`
- Per-instance tags: `arizuko-agent-<name>:latest`
- `CONTAINER_IMAGE` in `.env` selects the tag
- Selective upgrades: tag + restart one instance

## Persona files (shipped)

- `ant/CLAUDE.md` seeded to `~/.claude/CLAUDE.md`
- `ant/skills/` seeded to `~/.claude/skills/`
- Group folder: `SOUL.md`, `CLAUDE.md`, `facts/`
- Tier 2/3: ro mounts over inherited files

## Persona plugins (deferred)

Plugin composition (multiple plugins layered into one group) is
deferred. Products (`products.md`) are the shipped alternative: one
curated persona + skill set per group, selected at creation.
