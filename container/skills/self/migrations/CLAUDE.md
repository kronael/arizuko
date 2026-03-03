# Migration system

Migration files are named `NNN-description.md` and run in numeric order.

## How it works

1. Read `~/.claude/skills/self/MIGRATION_VERSION` (0 if missing)
2. List `~/.claude/skills/self/migrations/*.md`, sort numerically
3. Run all migrations with number > current version, in order
4. After each: `echo "N" > ~/.claude/skills/self/MIGRATION_VERSION`
5. Main group also syncs updated MIGRATION_VERSION to all other
   groups' skill dirs at `/workspace/project/data/sessions/*/`

## Adding a migration

1. Create `NNN-description.md` with check + steps + verification
2. Update `MIGRATION_VERSION` file in this dir to `N`
3. Update hardcoded "Latest migration version" in `self/SKILL.md`
4. Rebuild the agent image

## Convention

Each migration file must include:

- **Goal**: what it does
- **Check**: idempotency guard (skip if already done)
- **Steps**: shell commands to run
- **After**: update MIGRATION_VERSION
