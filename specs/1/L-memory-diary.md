---
status: draft
---

<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Memory: Diary

Agent-written daily notes. The diary IS the task log.

## Two-layer model

| Layer     | Purpose   | Content                                   |
| --------- | --------- | ----------------------------------------- |
| MEMORY.md | Knowledge | Preferences, patterns, long-term projects |
| Diary     | Work log  | Tasks, progress, decisions                |

No work.md. MEMORY.md stays under 200 lines.

## Path

`groups/<folder>/diary/YYYYMMDD.md` mounted rw at `/home/node/diary/`.

## Diary YAML summary format

YAML `summary:` with 5 bullet points max (critical tasks only) +
`## HH:MM` entries (250 chars max). Gateway reads summaries for
session-start injection.

## Injection

On new session, inject diary summaries as XML:

```xml
<knowledge layer="diary" count="14">
  <entry key="20260308" age="today">summary</entry>
  ...
</knowledge>
```

14-day window until progressive summarization ships.

## Nudge triggers

- `/diary` skill (agent-initiated)
- PreCompact hook (automatic, resets turn counter)
- Every 100 turns (guard prevents loops)
