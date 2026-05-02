# Migration system

Migration files are named `NNN-vX.Y.Z-summary.md` (older files use
`NNN-summary.md`; the version segment became required at v0.33.2).
Files run in numeric order.

## How it works

1. Read `~/.claude/skills/self/MIGRATION_VERSION` (0 if missing)
2. List `~/.claude/skills/self/migrations/*.md`, sort numerically
3. `cat` every file with number > current version, in order
4. After each: `echo N > ~/.claude/skills/self/MIGRATION_VERSION`

## Adding a migration

**Every release adds one file**, including docs-only. The migration
is the single trigger for the auto-migrate hook in
`gateway.checkMigrationVersion`, which also drives the chat
broadcast. Stub body for no-skill-change releases is fine.

1. Create `NNN-vX.Y.Z-summary.md` — short, actionable, no ceremony
2. Bump `MIGRATION_VERSION` in this dir to `N`
3. Bump "Latest migration version" in `../SKILL.md`
4. Rebuild the agent image

Each file is `cat`'d to the agent as-is. Keep it terse — a behavior
change note, not a procedure manual. Stub example:

```markdown
## v0.33.2 — release-marker

No skill changes. File exists so auto-migrate fires and the
v0.33.2 CHANGELOG blockquote is broadcast to all groups.
```

Spec: `specs/4/P-personas.md ## Versioning`.
