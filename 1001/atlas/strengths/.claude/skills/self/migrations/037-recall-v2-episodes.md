---
description: Recall v2 CLI, episode injection, /compact-memories skill
---

# 037 — Recall v2, Episodes, Compact Memories

## Goal

Install recall v2 CLI config, compact-memories skill, and update recall
skill to v2 protocol.

## Check

```bash
[ -f ~/.recallrc ] && [ -d ~/.claude/skills/compact-memories ] && echo "skip" && exit 0
```

## Steps

```bash
# 1. Copy .recallrc seed if missing
[ -f ~/.recallrc ] || cp /workspace/self/container/.recallrc ~/.recallrc

# 2. Copy compact-memories skill
mkdir -p ~/.claude/skills/compact-memories
cp /workspace/self/container/skills/compact-memories/SKILL.md \
   ~/.claude/skills/compact-memories/SKILL.md

# 3. Update recall skill to v2
cp /workspace/self/container/skills/recall/SKILL.md \
   ~/.claude/skills/recall/SKILL.md

# 4. Create episodes directory
mkdir -p ~/episodes
```

## After

```bash
echo "37" > ~/.claude/skills/self/MIGRATION_VERSION
```

## Notes

- Gateway now injects `<episodes>` XML on session start (day/week/month)
- Episodes created by `/compact-memories` cron tasks with `context_mode: 'isolated'`
- Recall v2 does hybrid FTS5+vector search; falls back to v1 grep if CLI unavailable
