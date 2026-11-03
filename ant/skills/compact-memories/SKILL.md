---
name: compact-memories
description: >
  Distill one period of the folder's memory into one file: episodes
  (session transcripts, the folder seen from outside) or diary (the agent's
  own entries, the folder seen from inside), by day, week or month. USE on
  the scheduled cron prompt, or "compact", "roll up the week", "summarise
  last month". NOT mid-conversation (self-trigger forbidden), NOT to
  recompact an existing file unless the user names the period.
user-invocable: true
arg: <lens> <level> [period]
---

# Compact Memories

One technique, three lenses. A level is distilled from the level below it;
the lens is the point of view, never the method.

| lens     | day source                             | week, month source | sink                     |
| -------- | -------------------------------------- | ------------------ | ------------------------ |
| diary    | the agent, `/diary`, as it works       | the level below    | `~/diary/<period>.md`    |
| episodes | `~/.claude/projects/*/*.jsonl`         | the level below    | `~/episodes/<period>.md` |
| digest   | the chat rows of the folder's channels | the level below    | a post in the chat       |

The core package ships the diary and episodes lenses. A group's own `digest`
skill, where one exists, is the third lens for people reading a channel; it
follows this page and adds only its gather and post steps.

A period is the file name: `YYYYMMDD` day, `YYYY-WNN` ISO week, `YYYY-MM`
month. One flat directory per lens, no subdirectory per level.

## Protocol

### 1. Period

The target is the most recent fully elapsed unit before now (UTC), never the
current one: `date -u` and shell math (`date -u -d "<date>" +%G-W%V` for the
ISO week). A third argument overrides it, in the level's format
(`/compact-memories episodes week 2026-W18`); use it for a backfill, and it
also overwrites an existing file.

### 2. Sources

- `episodes day`: `~/.claude/projects/*/*.jsonl` with mtime on or after the
  target date, or lines from it (a session spans days); slice to the date.
- `diary day`: no compaction, `/diary` writes it.
- `week`: the lens's `YYYYMMDD.md` of the ISO week, Monday to Sunday.
- `month`: the lens's `YYYY-W*.md` whose Monday falls in the month.

### 3. Outcome

One JSONL line per run in `<lens dir>/.compact-log.jl`:

```json
{"ts":"2026-05-25T03:00:42Z","level":"week","store":"episodes","target_period":"2026-W21","sources_found":7,"outcome":"WRITE","output":"~/episodes/2026-W21.md"}
```

- `WRITE` — sources found and no file for the period, or a period argument.
- `SKIP_NO_SOURCES` — no source. Never write an empty file.
- `SKIP_EXISTS` — the file exists and no period argument was given.
- `SKIP_VALIDATION_FAIL` — a frontmatter check below failed; name the rule.

`task_run_logs.status='success'` says the prompt was dispatched; this log
says what happened.

### 4. Distill

Rank by what a person said or decided. Keep a correction verbatim, a
deliverable a person accepted, a blocker. Drop the agent's own conclusions,
routine runs (migrations, crons, `/solve`), dead-end debugging and
unconfirmed inferences. The file is shorter than its sources. The last bullet
of `summary:` is the one thing to work on next.

### 5. Write

```yaml
---
summary: >
  - Shipped discord adapter (user confirmed)
  - Corrected: "sam is not stake-o-matic" (Apr 14)
  - Next: confirm EU eligibility on the three new leads
period: '2026-W21'
type: week
store: episodes
sources:
  - 20260518.md
  - 20260519.md
aggregated_at: '2026-05-25T03:00:42Z'
---
```

Checks before writing: `period` in the level's format; `sources` bare file
names without duplicates; `summary` not empty; nothing but the frontmatter
above the closing `---`.

## Cron

Five tasks per group, seeded at creation (`store.SeedDefaultTasks`), each
`context_mode: isolated` with `owner` and `chat_jid` the folder itself. Check
the existing tasks before adding one.

| Prompt                             | Cron        |
| ---------------------------------- | ----------- |
| `/compact-memories episodes day`   | `0 2 * * *` |
| `/compact-memories episodes week`  | `0 3 * * 1` |
| `/compact-memories episodes month` | `0 4 1 * *` |
| `/compact-memories diary week`     | `0 3 * * 1` |
| `/compact-memories diary month`    | `0 4 1 * *` |

A cron run is silent: no chat output, no `<status>` block. The file and the
log line are the result.
