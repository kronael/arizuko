---
name: compact-memories
description: >
  Compress episodes (session transcripts) or diary entries into
  progressive day/week/month summaries. Run on schedule or manually.
user-invocable: true
arg: <store> <level>
---

# Compact Memories

Progressive compression: each level built from the level below. Two stores, same pattern.

## Stores

### Episodes (session transcripts → summaries)

| Level | Sources                              | Output                 |
| ----- | ------------------------------------ | ---------------------- |
| day   | `.claude/projects/*/*.jsonl` (all)   | `episodes/YYYYMMDD.md` |
| week  | `episodes/YYYYMMDD.md` (7 days)      | `episodes/2026-W11.md` |
| month | `episodes/2026-W*.md` (month weeks)  | `episodes/2026-03.md`  |

### Diary (work log → summaries)

| Level | Sources                         | Output                   |
| ----- | ------------------------------- | ------------------------ |
| week  | `diary/YYYYMMDD.md` (7 days)    | `diary/week/2026-W11.md` |
| month | `diary/week/2026-W*.md` (month) | `diary/month/2026-03.md` |

Diary has no "day" level — daily entries already exist.

## Protocol

### 1. Gather sources

**Episodes day** — target date = yesterday (UTC).

1. Glob `~/.claude/projects/*/*.jsonl` — all project dirs, not just one.
2. Filter files: only those with mtime on or after the target date.
3. **Date-filter within each file**: parse JSONL lines, extract timestamps.
   Only include content from the target date. Sessions spanning multiple
   days MUST be sliced — include only the target date's portion.
4. Skip files with zero lines in the target date range.

JSONL timestamp extraction: each line is a JSON object. Look for
`timestamp`, `created_at`, or infer from message ordering. User messages
(`"type":"user"`) and result messages (`"type":"result"`) reliably have
timestamps. When in doubt, use the session_log query below.

**Authoritative cross-check**: query the messages DB via MCP tool
`query_db` or Bash (`sqlite3 /workspace/store/messages.db`):
```sql
SELECT sender, substr(content, 1, 200), timestamp
FROM messages WHERE date(timestamp) = 'YYYY-MM-DD'
ORDER BY timestamp
```
This catches interactions the transcripts might miss (MCP tool calls,
steered messages, scheduled tasks). Use both sources.

**Episodes week/month**: Glob the lower-level episode files for
the target period.

**Diary week/month**: Glob `diary/*.md` or `diary/week/*.md` for
the target period.

No sources → stop. Never write empty files.

### 2. Compress

Keep:

- Decisions made and why
- Deliverables shipped
- Active work streams
- Blockers and resolutions
- Who was involved

Drop:

- Routine operations (migrations that just ran, cron triggers)
- Dead-end debugging
- Conversation mechanics
- Duplicates across sources

Each level is shorter than the sum of its sources.

### 3. Write

Output file: `episodes/YYYYMMDD.md` (always YYYYMMDD, no hyphens for day level).

```markdown
---
summary: >
  - Shipped discord channel support
  - Resolved telegram auth token rotation
period: 'YYYYMMDD'
type: day
store: episodes
sources:
  - 79e60b7d-3fe0-4a2d-a529-c9e84241aeb6.jsonl
  - 64d579d2-c2bb-449e-81bd-7070445054b1.jsonl
aggregated_at: '2026-03-17T02:00:00Z'
---

## Key decisions

- Discord uses same ChannelOpts as telegram

## Deliverables

- Discord adapter shipped and tested

## Active work

- /recall spec v2 design

## Blockers

- None
```

`summary:` — dense, for `/recall` and gateway injection.
`sources:` — transcript filenames (not full paths).
`store:` — `episodes` or `diary`.
`period:` — YYYYMMDD for day, YYYY-WNN for week, YYYY-MM for month.

## Usage

```
/compact-memories episodes day
/compact-memories episodes week
/compact-memories episodes month
/compact-memories diary week
/compact-memories diary month
```

## Cron setup

On-demand. Set up when the user or agent wants progressive compression
for this group. Call `schedule_task` for each level:

| Prompt                             | Cron        | When               |
| ---------------------------------- | ----------- | ------------------ |
| `/compact-memories episodes day`   | `0 2 * * *` | daily 02:00        |
| `/compact-memories episodes week`  | `0 3 * * 1` | Monday 03:00       |
| `/compact-memories episodes month` | `0 4 1 * *` | 1st of month 04:00 |
| `/compact-memories diary week`     | `0 3 * * 1` | Monday 03:00       |
| `/compact-memories diary month`    | `0 4 1 * *` | 1st of month 04:00 |

All tasks use `contextMode: "isolated"` (fresh container, no session history).
`targetJid` = the chat JID that should trigger this group.

Example — set up all five:

```
schedule_task({ targetJid: "<group-jid>", prompt: "/compact-memories episodes day", cron: "0 2 * * *", contextMode: "isolated" })
schedule_task({ targetJid: "<group-jid>", prompt: "/compact-memories episodes week", cron: "0 3 * * 1", contextMode: "isolated" })
schedule_task({ targetJid: "<group-jid>", prompt: "/compact-memories episodes month", cron: "0 4 1 * *", contextMode: "isolated" })
schedule_task({ targetJid: "<group-jid>", prompt: "/compact-memories diary week", cron: "0 3 * * 1", contextMode: "isolated" })
schedule_task({ targetJid: "<group-jid>", prompt: "/compact-memories diary month", cron: "0 4 1 * *", contextMode: "isolated" })
```

Check existing tasks first — don't create duplicates.
