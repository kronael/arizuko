# Memory: Episodes — open (v2)

Hierarchical time-based aggregation of diary entries. Automatic, always on.
Feeds long-term/facts memory. Not planned for v1.

## What it is

Episodes are higher-level summaries produced by aggregating diary entries
upward through a time hierarchy:

```
diary/20260305.md  ─┐
diary/20260304.md  ─┤→ episodes/2026-W09.md  (week)
diary/20260303.md  ─┘
                         ↓
                   episodes/2026-03.md  (month)
                         ↓
                   episodes/2026.md     (year)
```

Each level compresses the level below using a silent agent turn or
scheduled task. Agent decides what to keep at each compression step.

## Push (auto-injected)

On session reset, the gateway could inject the current week/month episode
summary alongside the diary pointer. Not yet designed — depends on how
large these summaries grow.

## Pull (on demand)

Agent reads episode files directly:

```
/workspace/group/episodes/2026-W09.md
/workspace/group/episodes/2026-03.md
```

MCP tool `get_episode(period)` could expose a structured query interface —
not yet defined.

## Trigger

Scheduled task (using existing task-scheduler.ts) fires:

- Daily: aggregate yesterday's diary entry into the current week file
- Weekly: aggregate the week into the month file
- Monthly: aggregate the month into the year file

One agent invocation per aggregation step. Sequential (one agent per group).

## Relationship to other layers

- **Input**: diary entries (`specs/v1/memory-diary.md`)
- **Output**: feeds long-term/facts (`specs/v2/memory-facts.md`)
- **Managed memory**: MEMORY.md and CLAUDE.md are separate
  (`specs/v2/memory-managed.md`) — agent may choose to promote episode
  content to MEMORY.md manually

## Prior art

- **Muaddib**: autochronicler triggers every ~10 interactions, batches
  100 messages, sends to external LLM for summarization (1024 token cap).
  External LLM does the compression; agent is passive. Our approach:
  agent runs the compression turn itself via scheduled task.
- **brainpro**: `memory/YYYY-MM-DD.md` daily notes, today + yesterday
  auto-loaded. No explicit weekly/monthly hierarchy.

## Open

- Define episode file format (YAML frontmatter + body, like diary?)
- Aggregation prompt design — what the agent is asked to do at each level
- Whether the gateway injects episode summaries on reset (in addition to
  diary pointer) and at what granularity
- `get_episode` MCP tool interface
- Retention policy — keep all episode files forever, or prune old ones?
- Whether to use external LLM (muaddib-style) or agent-self (our approach)
- Review atlas system approach before implementing facts integration
