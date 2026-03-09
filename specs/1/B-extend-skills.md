# Skills -- shipped

Markdown instruction sets loaded into every agent container.

## Layout

```
container/skills/
  self/         -- identity, memory, system messages
  migrate/      -- skill sync + migration (main group only)
  whisper/      -- voice transcription
  <name>/
    SKILL.md    -- required; YAML frontmatter + instructions
    *.md        -- optional reference files
    *.sh / *.ts -- optional executable helpers
```

## Seeding -- shipped

On first container spawn per group, gateway copies
`container/skills/` to `sessions/<group>/.claude/skills/`.
Only runs if destination doesn't exist. Agent can modify
its copy — changes persist across restarts.

Canonical definitions at `/workspace/self/container/skills/`
(read-only mount).

## Updates via /migrate -- shipped

`/migrate` skill (main group only):

1. Compares each `SKILL.md` in canonical vs `~/.claude/skills/`
   for every group session
2. Copies entire skill dir if missing or changed
3. Runs pending migrations from
   `container/skills/self/migrations/` (numbered, tracked
   by `MIGRATION_VERSION`)

## SKILL.md format

```markdown
---
name: skill-name
description: one-line summary
triggers: [keyword1, keyword2]
---

Skill instructions here...
```

## Rules

- Naming: `^[a-z0-9\-]+$`, validated at seeding time
- Migration failure: stop on first error, retry from
  that migration on next `/migrate` run

## Open

- Skills proposed by agent via plugin flow (`plugins.md`)
