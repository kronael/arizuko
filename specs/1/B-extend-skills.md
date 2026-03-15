<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Skills

Markdown instruction sets loaded into every agent container.

## SKILL.md format

```markdown
---
name: skill-name
description: one-line summary
triggers: [keyword1, keyword2]
---

Skill instructions here...
```

YAML frontmatter required. `triggers` is an array of keywords.

## Rules

- Naming: `^[a-z0-9\-]+$`, validated at seeding time
- Migration failure: stop on first error, retry from that
  migration on next `/migrate` run

## Layout

```
container/skills/
  self/         -- identity, memory, system messages
  migrate/      -- skill sync + migration (main group only)
  whisper/      -- voice transcription
  <name>/
    SKILL.md    -- required
    *.md        -- optional reference files
    *.sh / *.ts -- optional executable helpers
```
