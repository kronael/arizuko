---
status: shipped
---

# Memory levels — one format, two lenses, one technique

## What it is

A memory file is a Markdown file with a YAML `summary:` whose name is its
period — `YYYYMMDD.md`, `YYYY-WNN.md`, `YYYY-MM.md` — in one flat directory
per lens. The diary lens (`~/diary/`) is the folder seen from inside: what
the agent did and decided. The episodes lens (`~/episodes/`) is the folder
seen from outside: the exchanges, read off the session transcripts. One
skill, `compact-memories <lens> <level> [period]`, distills every level above
the first with one technique. The newest file of each lens and level reaches
a turn when it starts.

## The problem

- Nothing reads the levels. v0.72.0 (`98b3b2678`) stopped `runed/render.go`
  from pushing `<episodes>` and `<knowledge layer="diary">` into every turn,
  because 23 folder-wide summaries in every thread's prompt made an agent
  continue another thread's topic, and the block had grown to 22 KB per turn
  (BUGS `L86`). The replacement is `/recall-memories`, and no agent calls it:
  in the 7 days to 2026-11-03, 0 of 141 krons sessions and 1 of 274 sloth
  sessions did. The crons write `~/episodes/20261102.md`, `2026-W43.md`,
  `2026-10.md` on krons; no turn sees them.
- Two layouts for one shape. `ant/skills/compact-memories/SKILL.md:42-43`
  writes diary rollups to `~/diary/week/` and `~/diary/month/`, episodes
  rollups flat beside their day files; the skill carries the technique twice,
  once per store, and the sloth `digest` skill carries it a third time.
- The owner's own diary (`kronael/tools`, `skills/diary`) has day files and
  no levels at all.

## Where it fits

```
                 ┌──────────────────┐ day (changed)    ┌──────────────────┐
session .jsonl ─►│ compact-memories │─────────────────►│ ~/episodes/      │
                 │ episodes <level> │ week, month      │ YYYYMMDD.md      │
                 │                  │─────────────────►│ YYYY-WNN.md      │
                 └──────────────────┘                  │ YYYY-MM.md       │
                                                       └────────┬─────────┘
/diary ─────────────────────────────► ~/diary/YYYYMMDD.md       │
                 ┌──────────────────┐ week, month (changed)     │
                 │ compact-memories │─────────────────►┌────────┴─────────┐
                 │ diary <level>    │                  │ ~/diary/         │
                 └──────────────────┘                  │ YYYY-WNN.md      │
                                                       │ YYYY-MM.md       │
                                                       └────────┬─────────┘
                                                                │ newest per
                                                                │ lens+level (new)
                                                                ▼
                                   ┌──────────────────────────────────────┐
         first prompt of a session │ hooks/prompt_context.py              │
         or after 30 idle min ────►│ <memory scope="folder"> (new)        │
                                   │ <knowledge layer="work">  <user-memory>
                                   └──────────────────────────────────────┘
```

```yaml
- path: ant/skills/compact-memories/SKILL.md (changed)
  job: the one technique — period, sources, outcome, distill, write — and the lens table
  names:
    - '/compact-memories <lens> <level> [period]'
    - 'lens: diary | episodes | digest; level: day | week | month'
  never: reads a level it does not write; writes outside the lens directory
- path: ant/hooks/prompt_context.py (changed)
  job: adds the newest summary of each lens and level when a session starts or resumes after idle
  names:
    - 'memory(home) -> str  # <memory scope="folder"> with <entry lens= level= period=>'
    - 'FILE: YYYYMMDD | YYYY-WNN | YYYY-MM'
  never: reads a subdirectory; emits on a prompt that continues a live session
- path: ant/hooks/solve_nudge.py (changed)
  job: owns the one condition both hooks share
  names:
    - 'new_task(data, now) -> bool  # first prompt of the transcript, or idle >= IDLE_SECONDS'
  never: keeps state
- path: ant/skills/diary/SKILL.md (changed)
  job: the day file of the diary lens
  never: promises a nudge the harness does not send (BUGS L500)
- path: diary/README.md (changed)
  job: the memory layers figure, twinned on concepts/ant.html
```

## The levels reach the turn that starts

```
UserPromptSubmit ──► solve_nudge.py ──► new_task? ──► "/solve" nudge
                 └─► prompt_context.py ─┬─ every prompt: <knowledge layer="work"> <user-memory>
                                        └─ new_task (new): <memory scope="folder">
                                             <entry lens="episodes" level="day"   period="20261102">…</entry>
                                             <entry lens="episodes" level="week"  period="2026-W43">…</entry>
                                             <entry lens="episodes" level="month" period="2026-10">…</entry>
                                             <entry lens="diary"    level="day"   period="20261022">…</entry>
                                             <entry lens="diary"    level="week"  period="2026-W43">…</entry>
                                             <entry lens="diary"    level="month" period="2026-10">…</entry>
```

Current, `ant/hooks/prompt_context.py:19`:

```python
def context(prompt, home):
    layers = []
    work = read(home / 'work.md')
```

Proposed:

```python
def context(prompt, home, fresh):
    layers = []
    if fresh:
        layers.append(memory(home))
    work = read(home / 'work.md')
```

The block is at most six summaries and does not grow with history, so the
22 KB crowd-out cannot return. It arrives only where the agent orients — the
first prompt of a thread's session and the first after an idle gap — because
every later prompt of the session already has it in the transcript, and
`new_task` is the condition the `/solve` nudge already uses. The manual keeps
the thread primary: the block is labelled `scope="folder"` and
`ant/CLAUDE.md` § Solve says a folder-wide layer never continues another
thread's topic. Caveat: a day-level summary names yesterday's threads; the
label and the rule carry the separation, nothing filters by thread.

## One technique, a lens table

Current, `ant/skills/compact-memories/SKILL.md:30-43`: two store sections,
each with its own level table; diary rollups under `~/diary/week/` and
`~/diary/month/`; "Diary has no day level — operator-written daily entries
already exist".

Proposed: one protocol — period (last fully elapsed UTC unit), sources,
outcome (`WRITE` / `SKIP_NO_SOURCES` / `SKIP_EXISTS`, one JSONL line in
`<lens>/.compact-log.jl`), distill, write — and one table that gives each
lens its sources and its sink:

| lens     | day source                             | week, month source | sink                        |
| -------- | -------------------------------------- | ------------------ | --------------------------- |
| diary    | the agent, `/diary`, as it works       | the level below    | `~/diary/<period>.md`       |
| episodes | `~/.claude/projects/*/*.jsonl`         | the level below    | `~/episodes/<period>.md`    |
| digest   | the chat rows of the folder's channels | the level below    | a post in the chat; no file |

Distill is the same at every level and in every lens: rank by what a person
said or decided; keep corrections verbatim, deliverables a person accepted and
blockers; drop the agent's own conclusions and routine runs; each file shorter
than its sources; the last bullet of `summary:` is the one thing to work on
next. The lens is the point of view, not the method. `digest` is the lens the
sloth `digest` skill implements today for people reading a channel; it is in
the table so it shares the method, and the core package ships no digest cron.

The diary rollups move beside the day files because the period is already in
the name and `type:` in the frontmatter, so a subdirectory encodes the level a
second time; one flat directory per lens is what the hook, `/recall-memories`
and the owner's tools read. A file an instance already holds under
`~/diary/week/` or `~/diary/month/` stays where it is until the operator moves
it; the skill targets only the last completed period, so it never rewrites one.

## The owner's tools share the diary lens

`kronael/tools` ships `skills/diary` (the day file) and gains
`skills/compact-memories` with the diary lens of the same table, paths under
`~/.claude/projects/<slug>/diary/`. Its Stop hook (`hooks/stop.py`) already
nudges `/diary` when today's file is missing or stale; it now also nudges
`/compact-memories diary week` when the last completed ISO week has day files
and no `YYYY-WNN.md`, and `diary month` likewise, on the same stamp and
interval. arizuko installs crons; the tools nudge. Episodes stay an arizuko
lens: the tools have no transcript store a turn would read from outside.
