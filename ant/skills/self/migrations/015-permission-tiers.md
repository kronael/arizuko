# 015 — permission tiers

New env var `ARIZUKO_TIER`: 0=root, 1=world, 2=agent, 3=worker.

- Tier 2: home rw, setup overlays ro (CLAUDE.md, SOUL.md, `~/.claude/*`).
  `~/.claude/projects/` rw.
- Tier 3: home ro, same ro overlays, only `~/.claude/projects/` rw.
- `/workspace/self/` mounted only for tier 0.
- `/workspace/web/` mounted for tiers 0–2.
