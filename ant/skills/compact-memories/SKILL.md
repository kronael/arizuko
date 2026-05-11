---
name: compact-memories
description: >
  Compress episodes (session transcripts) or diary entries into progressive
  day/week/month summaries. USE on scheduled cron prompt, or "compact",
  "summarise yesterday", "roll up the week". NOT mid-conversation
  (self-trigger forbidden), NOT to recompact existing output unless user
  asks (lossy + expensive).
user-invocable: true
arg: <store> <level>
---

# Compact Memories

Progressive compression: each level built from the level below.

## Stores

### Episodes (session transcripts → summaries)

| Level | Sources                             | Output                 |
| ----- | ----------------------------------- | ---------------------- |
| day   | `.claude/projects/*/*.jsonl` (all)  | `episodes/YYYYMMDD.md` |
| week  | `episodes/YYYYMMDD.md` (7 days)     | `episodes/2026-W11.md` |
| month | `episodes/2026-W*.md` (month weeks) | `episodes/2026-03.md`  |

### Diary (work log → summaries)

| Level | Sources                         | Output                   |
| ----- | ------------------------------- | ------------------------ |
| week  | `diary/YYYYMMDD.md` (7 days)    | `diary/week/2026-W11.md` |
| month | `diary/week/2026-W*.md` (month) | `diary/month/2026-03.md` |

Diary has no "day" level — daily entries already exist.

## Protocol

### 1. Gather sources

**Episodes day** — target date = yesterday (UTC).

1. Glob `~/.claude/projects/*/*.jsonl` — all project dirs.
2. Keep files with mtime on or after the target date.
3. Parse JSONL lines and slice to the target date only. Sessions
   spanning multiple days MUST be sliced. Skip files with zero
   lines in range.

Events with `role:"user"` mix inbound messages (`<messages><message ...>`)
and tool-result turns — count both. Trust the DB over parsed transcripts:
cross-check via `inspect_messages chat_jid:=<jid>` for each route JID
listed by `inspect_routing`. For per-topic slices use
`get_thread chat_jid:=<jid> topic:=<topic>`.

**Episodes week/month / diary week/month**: glob the lower-level files
for the target period.

No sources → stop. Never write empty files.

### 2. Compress

Preserve what the user corrected, not what the agent concluded. User
corrections are authoritative; conclusions get redrawn every recall.

Keep: user corrections (verbatim), preferences, accepted deliverables
(shipped/merged/confirmed), flagged blockers.

Drop: agent conclusions, routine ops (migrations, cron, /resolve calls),
dead-end debugging, unconfirmed inferences.

Each level is shorter than the sum of its sources.

### 3. Write

Output file: `episodes/YYYYMMDD.md` (no hyphens for day level).

```markdown
---
summary: >
  - Shipped discord adapter (user confirmed)
  - Corrected: "sam is not stake-o-matic" (Apr 14)
period: 'YYYYMMDD'
type: day
store: episodes
sources:
  - 79e60b7d-3fe0-4a2d-a529-c9e84241aeb6.jsonl
aggregated_at: '2026-03-17T02:00:00Z'
---

## Corrections

- user: "sam is not stake-o-matic" (agent had conflated the two)
- user: "use ~ instead of /home/node/ in all paths"

## Shipped

- Discord adapter (user merged)

## Unresolved

- Map links regressed again on <component>
```

`summary:` — dense, leads with corrections.
`sources:` — filenames, not full paths.
`period:` — YYYYMMDD (day), YYYY-WNN (week), YYYY-MM (month).

## Usage

```
/compact-memories episodes day|week|month
/compact-memories diary week|month
```

## Cron setup

On-demand. Check existing tasks first — don't duplicate.
All tasks use `contextMode: "isolated"`. `targetJid` = group JID.

| Prompt                             | Cron        | When               |
| ---------------------------------- | ----------- | ------------------ |
| `/compact-memories episodes day`   | `0 2 * * *` | daily 02:00        |
| `/compact-memories episodes week`  | `0 3 * * 1` | Monday 03:00       |
| `/compact-memories episodes month` | `0 4 1 * *` | 1st of month 04:00 |
| `/compact-memories diary week`     | `0 3 * * 1` | Monday 03:00       |
| `/compact-memories diary month`    | `0 4 1 * *` | 1st of month 04:00 |
