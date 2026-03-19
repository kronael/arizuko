# 034 — Code Research Agent Spec

Informational migration — no code changes required.

## What changed

- Merged specs `4/H-researcher` and `4/3-support` into `3/3-code-research.md`
- `container/CLAUDE.md` Knowledge section tightened with strict relevance rule:
  facts must answer the question 100% correctly with only trivial application,
  or the agent must research via `/facts`
- `4/K-versioning-personas.md` gained "Shipped Worlds" section describing
  world templates as future direction for product configs

## Action required

None — this is a documentation-only change. The strict relevance rule in
`~/.claude/CLAUDE.md` will be updated on next `/migrate` run (CLAUDE.md
is seeded once, so existing groups need manual update or re-seed).

To manually apply the Knowledge section update:

```bash
# Read the updated Knowledge section
grep -A 10 "# Knowledge" ~/.claude/CLAUDE.md
# If it says "search facts/ for relevant knowledge", update it to:
# "scan facts/ summaries in <think>. Use a fact ONLY if it answers
# the question 100% correctly..."
```
