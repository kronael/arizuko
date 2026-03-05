# Skills — shipped

Skills are markdown-based instruction sets loaded into every agent container.
They define agent behaviour, capabilities, and conventions.

## Layout

```
container/skills/
  self/         — identity, memory conventions, system messages
  migrate/      — skill sync and migration runner (main group only)
  whisper/      — voice transcription
  <name>/       — any additional skill
    SKILL.md    — required; YAML frontmatter + instructions
    *.md        — optional reference files
    *.sh / *.ts — optional executable helpers
```

## Seeding — shipped

Skills are seeded at runtime, not baked into the image. On first container
spawn per group, gateway copies:

```typescript
fs.cpSync('container/skills/', `${DATA_DIR}/sessions/<group>/.claude/skills/`, {
  recursive: true,
});
```

Only runs if destination doesn't exist. Agent can modify its own copy —
changes persist across container restarts (same session dir).

`/workspace/self` = kanipi source, mounted read-only. Canonical skill
definitions always available at `/workspace/self/container/skills/`.

## Updates via /migrate — shipped

The `/migrate` skill (main group only) propagates skill updates across all
groups:

1. Compares each skill's `SKILL.md` in `/workspace/self/container/skills/`
   against `~/.claude/skills/` for every group session
2. Copies entire skill dir if missing or `SKILL.md` changed
3. Runs pending migrations from `container/skills/self/migrations/` —
   numbered files, applied in order, tracked by `MIGRATION_VERSION`

Run `/migrate` after shipping any skill change.

## SKILL.md format

```markdown
---
name: skill-name
description: one-line summary
triggers: [keyword1, keyword2] # optional
---

Skill instructions here...
```

Agent discovers skills via `~/.claude/skills/` which the SDK loads
automatically as project memory.

## Open

- Skills proposed by agent via plugin flow (`specs/v1/plugins.md`) —
  not yet implemented
