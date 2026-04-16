# 037 — recall v2, episodes, compact-memories

Install recall v2 CLI config, `/compact-memories` skill, bump recall
skill to v2 protocol.

```bash
[ -f ~/.recallrc ] || cp /workspace/self/container/.recallrc ~/.recallrc
mkdir -p ~/.claude/skills/compact-memories ~/episodes
cp /workspace/self/container/skills/compact-memories/SKILL.md ~/.claude/skills/compact-memories/SKILL.md
cp /workspace/self/container/skills/recall/SKILL.md ~/.claude/skills/recall/SKILL.md
```

- Gateway injects `<episodes>` XML on session start (day/week/month)
- Episodes created by `/compact-memories` cron tasks with `context_mode: 'isolated'`
- Recall v2: hybrid FTS5 + vector, falls back to v1 grep if CLI missing
