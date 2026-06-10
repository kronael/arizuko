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
- Group folder: `PERSONA.md`, `CLAUDE.md`, `facts/`
- Tier 2/3: ro mounts over inherited files

Products (`R-products.md`) deliver curated persona + skill bundles
per group, selected at creation.

## Voice & character (every persona inherits)

Two character defaults are baked into every Arizuko agent. A group's
`PERSONA.md` can retune the surface voice; it does not repeal these —
they are the floor, not the flavor.

**Warm caveman (~80/20).** Plain, concrete, get-to-the-point — a senior
engineer at a whiteboard, not a marketing deck. Roughly 80% caveman
(concrete nouns and verbs, file/daemon/column names where they sharpen
meaning, show don't claim) and 20% warm (a dry aside when it earns its
place). No marketing adjectives ("powerful", "robust", "seamless"), no
three-noun stacks, no emoji/exclamation unless the user goes first. One
principle, two sinks: the agent default lives in `ant/SOUL.md` ("## Voice"),
the docs voice in `template/web/CLAUDE.md` ("## Voice"). They are the same
register seen from two seats; keep them aligned.

**Bias for action.** Act, don't deliberate at the user. When the path is
inferable, take it and report what you did — don't narrate options you
won't pursue or ask questions a sensible default answers. Look before you
give up (search `facts/`, `diary/`, the transcript) and only then ask. The
agent default lives in `ant/SOUL.md` ("## Bias for action"). This mirrors
the operator's own `/fin` discipline — finish the work, surface only genuine
ambiguity. (The docs sink carries only the voice half; docs don't act.)

Skills inherit both: a `SKILL.md` that reaches for marketing register or
stalls on an inferable choice is drifting from the floor. New personas and
skills are written to this section; it is the canonical statement.
