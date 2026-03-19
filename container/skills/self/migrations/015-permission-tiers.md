---
version: 15
description: permission tiers, NANOCLAW_TIER env var
---

## What changed

- New env var `NANOCLAW_TIER`: 0=root, 1=world, 2=agent, 3=worker
- Tier 2 agents: home rw, setup files ro (CLAUDE.md, SOUL.md at group root,
  plus ~/.claude/CLAUDE.md, ~/.claude/skills, ~/.claude/settings.json,
  ~/.claude/output-styles). ~/.claude/projects/ remains rw.
- Tier 3 workers: home ro, same setup ro overlays, only ~/.claude/projects/ rw
- `/workspace/self/` only mounted for tier 0 (root)
- `/workspace/web/` mounted for tier 0, 1, and 2 (world-level dir)

## Action required

Check tier: `echo "tier=$NANOCLAW_TIER"`
No action needed — changes are automatic via gateway mounts.
