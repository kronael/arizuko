---
status: shipped
---

# Memory: Diary

Agent-written daily notes. The diary IS the task log. Reader: `diary/`.

## Two-layer model

| Layer     | Purpose   | Content                                   |
| --------- | --------- | ----------------------------------------- |
| MEMORY.md | Knowledge | Preferences, patterns, long-term projects |
| Diary     | Work log  | Tasks, progress, decisions                |

MEMORY.md stays under 200 lines. A third file for _in-flight_ state
landed later and is a separate layer —
[`../3/W-work.md`](../3/W-work.md).

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

Window is 14 entries (`diary.Read(groupDir, 14)`, called from
`container/runner.go`). Week/month rollups exist but are **not**
injected — the daily window already covers that span, so injecting both
would spend context on the same events twice. They exist so
`/recall-memories` can search longer timeframes
([`../4/17-knowledge-system.md`](../4/17-knowledge-system.md)).

## Nudge triggers

- `/diary` skill (agent-initiated)
- PreCompact hook (automatic, resets turn counter)
- Every 100 turns (guard prevents loops)
