# diary

The diary summary reader, and the memory layers it is one of.

## The memory layers

<!-- twin:memory -->

```
layer        file in the group home           reaches the turn
──────────── ──────────────────────────────── ─────────────────────────────
now          ~/work.md                        pushed whole, every turn
today        ~/diary/YYYYMMDD.md              pulled: /recall-memories
day, week,   ~/episodes/YYYYMMDD.md,          pulled: /recall-memories
month        YYYY-WNN.md, YYYY-MM.md
people       ~/users/<id>.md                  pushed whole for the sender,
                                              <user-memory>
facts        ~/facts/*.md                     pulled: /recall-memories,
                                              then /find
index        ~/.claude/projects/-home-node/   read by Claude Code itself
             memory/MEMORY.md

session transcripts ──► /compact-memories episodes day ──► ~/episodes/
~/episodes/ day files ──► week ──► month ─────────────────► ~/episodes/
~/diary/ daily files ──► /compact-memories diary week ───► ~/diary/week/
~/diary/week/ ──► diary month ────────────────────────────► ~/diary/month/
    all five run as timed tasks with context_mode isolated;
    ~/diary/week/ and ~/diary/month/ are only pulled
```

<!-- /twin -->

A layer is pushed when it belongs to every turn of the folder or the sender,
and pulled when it spans threads or is too large. The core
package's `prompt_context.py` hook (`ant/hooks/prompt_context.py`) reads the
pushed layers off the home on every UserPromptSubmit; the table of every
block is `routd/README.md` § Prompt assembly.

- **Work, now.** `~/work.md` is injected whole as `<knowledge
layer="work">`, with no truncation and no age check. A stale file is the
  agent's to clear; nothing nudges it.
- **Diary and episodes, pulled.** `~/diary/` and `~/episodes/` cover the
  whole folder, not one thread, so `/recall-memories` reads them on demand
  and the hook does not.
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
(`store.SeedDefaultTasks`, `store/tasks.go:452`): `/compact-memories
episodes day`, `episodes week`, `episodes month`, `diary week` and `diary
month`. Each runs on `timed` with `context_mode: isolated`, because a
compaction turn that shared the group's session would summarise its own
summarising.

## Public API

- `ExtractSummary(path string) string` — parse frontmatter `summary:`

## Dependencies

None (stdlib only).

## Files

- `diary.go`
