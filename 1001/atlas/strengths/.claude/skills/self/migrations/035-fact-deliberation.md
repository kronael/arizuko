# 035 — Fact Deliberation in Think Blocks

Informational migration — CLAUDE.md Knowledge section updated.

## What changed

`container/CLAUDE.md` Knowledge section now requires structured deliberation
in `<think>` blocks before answering from facts:

1. List candidate facts found by scanning summaries
2. For each candidate, explain what it says, whether it answers the question,
   and what gaps remain
3. Reach a verdict: use the fact, refresh via `/facts`, or research fresh

Previously agents were told to "decide in `<think>` first" but could skip
explaining their reasoning. Now the deliberation steps are mandatory.

## Action required

Update `~/.claude/CLAUDE.md` Knowledge section. The key addition is the
3-step deliberation block after the decision tree. On next `/migrate`,
CLAUDE.md will be re-seeded for new groups. Existing groups need manual
update:

```bash
# Check current Knowledge section
grep -A 20 "# Knowledge" ~/.claude/CLAUDE.md
# If it says "Always decide in <think> first" without the 3-step list,
# it needs updating to the new deliberation format.
```
