# diary

The diary summary reader, and the memory layers it is one of.

## The memory layers

<!-- twin:memory -->

```
layer        file in the group home           reaches the turn
──────────── ──────────────────────────────── ─────────────────────────────
now          ~/work.md                        pushed whole, every turn
diary        ~/diary/YYYYMMDD.md,             newest of each level pushed
day, week,   YYYY-WNN.md, YYYY-MM.md          on a new task, <memory
month                                         scope="folder">; the rest:
episodes     ~/episodes/YYYYMMDD.md,          /recall-memories
day, week,   YYYY-WNN.md, YYYY-MM.md
month
people       ~/users/<id>.md                  pushed whole for the sender,
                                              <user-memory>
facts        ~/facts/*.md                     pulled: /recall-memories,
                                              then /find
index        ~/.claude/projects/-home-node/   read by Claude Code itself
             memory/MEMORY.md

/diary ──────────────────────────────────────────────► ~/diary/YYYYMMDD.md
session transcripts ─► /compact-memories episodes day ─► ~/episodes/YYYYMMDD.md
day files ─► /compact-memories <lens> week ─► YYYY-WNN.md ─► month ─► YYYY-MM.md
    one flat directory per lens; the five tasks run isolated
```

<!-- /twin -->

A layer is pushed whole when it belongs to every turn of the folder or the
sender. A layer that spans threads is pushed as its newest summary per level,
once per task, and pulled for the rest. The core package's `prompt_context.py`
hook (`ant/hooks/prompt_context.py`) reads the pushed layers off the home on
UserPromptSubmit; the table of every block is `routd/README.md` § Prompt
assembly.

- **Work, now.** `~/work.md` is injected whole as `<knowledge
layer="work">`, with no truncation and no age check. A stale file is the
  agent's to clear; nothing nudges it.
- **Diary and episodes, the levels.** One flat directory per lens; the
  period is the file name (`YYYYMMDD`, `YYYY-WNN`, `YYYY-MM`) and the YAML
  `summary:` is what a turn reads. On the first prompt of a session and the
  first after 20 idle minutes — the condition of the `/solve` nudge,
  `solve_nudge.new_task` — the hook adds the newest file of each lens and
  level as `<memory scope="folder">`, at most six entries. The block covers
  the whole folder, not one thread, so the manual says never to continue
  another thread's topic from it. A later prompt of the same session adds
  nothing: the block is already in the transcript.
- **People.** The platform puts `<user id="…"/>` in the prompt. The hook
  reads `~/users/<id>.md` for that id and emits the whole file as
  `<user-memory>`.
- **Facts.** `~/facts/` is never pushed. `/recall-memories` greps the
  `summary:` lines across `~/facts/`, `~/diary/`, `~/users/` and
  `~/episodes/` in a subagent, which judges each match. The agent then uses
  the match, refreshes it through `/find`, or researches fresh. There is no
  index and no full-text store behind the grep. `/recall-messages` searches
  the chat history instead.
- **MEMORY.md.** Claude Code keeps its own memory index under
  `~/.claude/projects/-home-node/memory/`. No arizuko code writes it or
  injects it.

## Writing the diary

The agent writes the diary through the `/diary` skill. An entry is
`~/diary/YYYYMMDD.md`: a YAML `summary:` naming the project, who the agent
works with and at most 5 critical open items, then `## HH:MM` entries of at
most 250 characters each.

One mechanism nudges the agent to write: the PreCompact hook
(`ant/src/claude.ts:236`). Before Claude Code compacts the context, the hook
archives the transcript to `~/conversations/` and tells the agent to invoke
`/diary` and to keep its memory files in the summary.

## Compaction

Five tasks per group compact the memory, seeded at group creation
(`store.SeedDefaultTasks`, `store/tasks.go:402`): `/compact-memories
episodes day`, `episodes week`, `episodes month`, `diary week` and `diary
month`. One skill carries the technique for both lenses
(`ant/skills/compact-memories/SKILL.md`): the last fully elapsed period, the
level below as the source, one outcome line in `<lens>/.compact-log.jl`,
corrections kept verbatim and the agent's own conclusions dropped. Each task
runs through the routd task ticker with `context_mode: isolated`, because a compaction turn that
shared the group's session would summarise its own summarising.

## Public API

- `ExtractSummary(path string) string` — parse frontmatter `summary:`

## Dependencies

None (stdlib only).

## Files

- `diary.go`
