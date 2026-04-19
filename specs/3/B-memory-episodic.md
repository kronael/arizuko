---
status: deferred
---

# Memory: Episodes

Mechanically produced summaries of past sessions. Aggregates diary
entries upward through a time hierarchy. Passive agent — scheduled
task runs compression.

## Hierarchy

```
diary/20260305.md  ─┐
diary/20260304.md  ─┤→ episodes/2026-W09.md
diary/20260303.md  ─┘
                         ↓
                   episodes/2026-03.md
                         ↓
                   episodes/2026.md
```

Each level compresses the one below via a silent agent turn.

## Push / pull

- Push: on session reset, inject current week/month summary alongside
  diary pointer. Granularity undecided.
- Pull: agent reads `/workspace/group/episodes/<period>.md` directly.
  Optional `get_episode(period)` MCP tool.

## Trigger

Scheduled task (existing task-scheduler):

- Daily → current week file
- Weekly → month file
- Monthly → year file

One agent invocation per step. Sequential per group.

## Why it matters

Long-running instances accumulate thousands of messages. Without
episodic summaries, cold-start resume replays full history. Week
summary should be sufficient for resume — design content accordingly
(decisions, active projects, open blockers). Handle sparse days.

## Open

- Episode file format (YAML frontmatter + body, like diary?)
- Aggregation prompt per level
- Whether gateway injects episode summaries on reset
- `get_episode` MCP interface
- Retention / pruning
- External LLM vs agent-self compression
