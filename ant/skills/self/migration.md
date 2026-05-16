# Skill seeding and migration

On first container spawn, gateway copies `/workspace/self/ant/skills/*`
and `/workspace/self/ant/CLAUDE.md` to `~/.claude/`. Canonical latest at
`/workspace/self/ant/skills/`. Run `/migrate` to sync updates and apply
pending migrations.

Latest migration version: **122**. Compare:

```bash
cat ~/.claude/skills/self/MIGRATION_VERSION
```

This file (`migration.md`) is the human doc; `MIGRATION_VERSION` (the
sibling numeric file) is the version number the `migrate` skill compares
against. Both live in `ant/skills/self/`.
