---
status: deferred
source: hermes-agent peel (2026-04-11)
overlaps_with: 5/d-self-improvement.md
---

# Self-eval via sub-query at container exit

> Overlaps with [`5/d-self-improvement.md`](../5/d-self-improvement.md).
> Both produce agent-driven critique + memory updates; this spec
> triggers via sub-`query()` at container exit, the other via `timed`
> cron. Treat as candidates for unification when either ships — pick
> one trigger shape, not both.

After main turn completes, run a second restricted `query()` in the
same container to review and persist findings. Fires every N
successful container runs per group via a counter in `groups.eval_counter`.

Trigger: new `groups.eval_counter INT DEFAULT 0`. Gateway increments
on successful run only; when ≥ `cfg.EvalInterval` (env
`ARIZUKO_EVAL_INTERVAL`, default 10, 0=disabled), sets
`reviewDue: true` on container input and resets counter.

Runner: `ant/src/review.ts` calls SDK `query()` with
`allowedTools: ['Read','Write','Edit','Glob']`, no MCP tools,
`maxTurns: 8`. Restricted system prompt: identity, workspace paths,
criteria, conservative framing. Writes only to `~/.claude/CLAUDE.md`
(bounded ~8KB) or `~/.claude/skills/<name>/SKILL.md`. Logs summary to
journal at INFO; no channel send.

Criteria (new skill `ant/skills/self-eval/SKILL.md`,
`user-invocable: false`):

- memory → durable user facts, expectations, env quirks (not task
  progress)
- skill → non-trivial approach with trial-and-error, tricky error
  fix, outdated-skill patch
- default: "Nothing to save" and stop

Main agent SOUL gets proactive-save guidance (Hermes
MEMORY_GUIDANCE + SKILLS_GUIDANCE equivalents) — review is the
safety net.

Rationale: arizuko's ephemeral containers can't run Hermes-style
background daemon thread; sync sub-query at container exit is the
equivalent.

Adaptations from Hermes: per-group DB counter (not in-memory session
counter); single merged memory+skill counter for v1; second
`query()` (not fresh AIAgent fork); log to journal (not
`💾 actions` to user); recursion impossible by design (only gateway
increments).

~400 LOC across `store`, `core/config`, `gateway`, `container/runner`,
`ant/src/index`, new `ant/src/review` + test, new skill, migration.

Unblockers: threshold tuning (10 may be too rare for DM workloads —
consider signal-driven: fire when tool-calls ≥3), race with skill-guard
([7-self-learning.md](7-self-learning.md)) composes naturally (Write
fails, counter still advances).

Phase 2: split memory/skill counters, per-group tunable interval,
dashd self-eval view, revert-log via git tag.
