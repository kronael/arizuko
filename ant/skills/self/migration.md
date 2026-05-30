# Skill seeding and migration

On group create, Go (`container.seedSkills`) copies
`/opt/arizuko/ant/skills/*` and `/opt/arizuko/ant/CLAUDE.md` to
`~/.claude/`, AND snapshots the same source files into
`~/.claude/.merge-base/`. The merge-base is the upstream version we
last synced; the live copy is what Claude Code reads.

When `MIGRATION_VERSION` is behind, the gateway enqueues `/migrate` on
the root group (per-spawn auto-cp was removed — the skill owns the
sync now). The skill walks each stock file and does a 3-way merge:

- `base` = `~/.claude/.merge-base/<path>` (last upstream synced)
- `ours` = `~/.claude/<path>` (live, possibly operator-edited)
- `theirs` = `/opt/arizuko/ant/<path>` (new upstream)

Outcomes per file: new upstream → copy; only upstream changed → copy;
only ours changed → keep ours; both changed → agent merges inline.
After any write, `theirs` overwrites `base` so the next sync's diff
is correct. Custom skills (those NOT under `/opt/arizuko/ant/`)
are never touched.

`<group>/CLAUDE.md` is the **operator-owned overlay** and is never
merged. `<group>/.claude/CLAUDE.md` is the agent-managed file.

To opt out of a stock skill, drop `~/.claude/skills/<name>/.disabled`.
seedSkills will skip the dir AND remove its `SKILL.md` so Claude
Code stops indexing it; the migrate skill skips it too.

Latest migration version: **153**. Compare:

```bash
cat ~/.claude/skills/self/MIGRATION_VERSION
```

This file (`migration.md`) is the human doc; `MIGRATION_VERSION` (the
sibling numeric file) is what the `migrate` skill compares against.
