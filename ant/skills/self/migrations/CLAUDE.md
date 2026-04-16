# Migration system

Migration files are named `NNN-description.md` and run in numeric order.

## How it works

1. Read `~/.claude/skills/self/MIGRATION_VERSION` (0 if missing)
2. List `~/.claude/skills/self/migrations/*.md`, sort numerically
3. `cat` every file with number > current version, in order
4. After each: `echo N > ~/.claude/skills/self/MIGRATION_VERSION`

## Adding a migration

1. Create `NNN-description.md` — short, actionable, no ceremony
2. Bump `MIGRATION_VERSION` in this dir to `N`
3. Bump "Latest migration version" in `../SKILL.md`
4. Rebuild the agent image

Each file is `cat`'d to the agent as-is. Keep it terse — a behavior
change note, not a procedure manual.
