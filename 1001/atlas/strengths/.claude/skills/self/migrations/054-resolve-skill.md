# 054 — resolve skill replaces dispatch

## Goal

Add `/resolve` skill and remove stale `/dispatch` nudge from CLAUDE.md.

## Check

```bash
[ -d ~/.claude/skills/resolve ] && echo "done" && exit 0
```

## Steps

```bash
# resolve skill is seeded automatically by seedSkills on container spawn.
# Just clean up the old dispatch references from group CLAUDE.md if present.
if grep -q 'Run `/dispatch`' ~/.claude/CLAUDE.md 2>/dev/null; then
  sed -i '/Run `\/dispatch`/d' ~/.claude/CLAUDE.md
fi
```

## After

```bash
echo 54 > ~/.claude/skills/self/MIGRATION_VERSION
```
