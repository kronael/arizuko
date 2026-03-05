# Memory: Episodes — open (v2)

Mechanically produced summaries of past sessions. Automatic, always on.
Feeds long-term/facts memory. Not planned for v1.

The agent is passive — a scheduled process takes raw transcripts or diary
entries and produces episode summaries without agent involvement. Contrast
with diary (v1) which is agent-written and subjective.

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

## Episode notes (REDACTED, Mar 2026)

The REDACTED instance ran a single session for 4+ days with no compaction and
no episodic summaries. Every container restart replayed 45+ messages from
raw JSONL. The startup cost grows linearly with session length; after 1000+
messages this becomes expensive.

**Why episodic memory matters here:**

- Without episodic summaries, the only context available after a crash is
  the full message history. For a 4-day project session, that's
  potentially thousands of messages to replay.
- The episodic layer (day → week aggregation) provides a compressed
  fallback: if the session is cold and no diary entry exists, a week summary
  gives the agent enough context to resume without replaying raw history.
- REDACTED's user (Czech entrepreneur, multi-day horse trail planning projects)
  is exactly the episodic-memory use case: not a chat bot, but a project
  assistant where session context spans weeks.

**Implication for episodic design:**

- Daily aggregation must be robust even when diary entries are sparse or
  absent (missed flush days). The aggregator should handle gaps gracefully.
- The week episode should be sufficient for a cold-start resume — design
  its content accordingly (decisions, active projects, open blockers).

## Open

- Define episode file format (YAML frontmatter + body, like diary?)
- Aggregation prompt design — what the agent is asked to do at each level
- Whether the gateway injects episode summaries on reset (in addition to
  diary pointer) and at what granularity
- `get_episode` MCP tool interface
- Retention policy — keep all episode files forever, or prune old ones?
- Whether to use external LLM (muaddib-style) or agent-self (our approach)
- Review atlas system approach before implementing facts integration
