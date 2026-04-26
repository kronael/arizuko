---
status: unshipped
overlaps_with: 6/8-self-eval-skill.md
---

# Self-improvement (agent-driven eval)

> Overlaps with [`6/8-self-eval-skill.md`](../6/8-self-eval-skill.md).
> Both target the same outcome (agent-produced critique + memory
> updates) with different triggers — this spec uses scheduled `timed`
> cron, 6/8 uses a sub-`query()` at container exit. Treat as
> candidates for unification when either ships; pick one trigger
> shape rather than both.

Scheduled self-eval per agent. Via `timed` cron, agent reads recent
`diary/*.md`, `logs/container-*.log`, `.ship/critique-*.md`; produces
observations → diary entry; writes improvement proposals to
`.ship/critique-<TOPIC>.md`; notifies operator via `send_message`.
No auto-apply — operator reviews.

Writes to `diary/` = what happened; `facts/` = verified knowledge;
`critique/` = what should change.

Rationale: reactive discovery today (user complains, circuit breaker
fires). Need proactive detection + structured proposals.

Superseded in part by
[../6/8-self-eval-skill.md](../6/8-self-eval-skill.md) (sub-query at
container exit) — that's a different trigger shape. Pick one before
shipping.

Open questions:

1. Degradation signals not tracked (latency trending up, apology
   frequency, tool-failure rate). New tables needed?
2. Per-agent self-improvement vs root-only aggregation vs
   operator-driven `/eval` skill only.
3. What the agent can propose: own SOUL/CLAUDE, new skills, grants,
   cron, routes. Root can propose system config.
