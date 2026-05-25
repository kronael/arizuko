---
name: compact-memories
description: >
  Compress episodes (session transcripts) or diary entries into progressive
  day/week/month summaries. USE on scheduled cron prompt, or "compact",
  "summarise yesterday", "roll up the week". NOT mid-conversation
  (self-trigger forbidden), NOT to recompact existing output unless user
  asks (lossy + expensive).
user-invocable: true
arg: <store> <level> [period]
---

# Compact Memories

Progressive compression: each level built from the level below.

## Shape vs. per-turn context reducers

This is arizuko's long-horizon **context reducer** — the same problem
other agent systems (e.g. muaddib's `src/rooms/command/context-reducer.ts`)
solve with a cheap-model condenser that runs *automatically per-turn*
on the running history. arizuko inverts the cadence: compaction is
**cron-driven, day/week/month**, writes to `episodes/` and `diary/`
files, and is reloaded via the diary block + `/recall-memories`. The
in-session window stays raw and is bounded instead by impulse-gate
batching plus Claude Code's own compaction.

## Stores

### Episodes (session transcripts → summaries)

| Level | Sources                                | Output                  |
| ----- | -------------------------------------- | ----------------------- |
| day   | `~/.claude/projects/*/*.jsonl`         | `~/episodes/YYYYMMDD.md` |
| week  | `~/episodes/YYYYMMDD.md` (7 days)      | `~/episodes/YYYY-WNN.md` |
| month | `~/episodes/YYYY-W*.md` (month weeks)  | `~/episodes/YYYY-MM.md`  |

### Diary (work log → summaries)

| Level | Sources                                | Output                       |
| ----- | -------------------------------------- | ---------------------------- |
| week  | `~/diary/YYYYMMDD.md` (7 days)         | `~/diary/week/YYYY-WNN.md`   |
| month | `~/diary/week/YYYY-W*.md` (month wks)  | `~/diary/month/YYYY-MM.md`   |

Diary has no "day" level — operator-written daily entries already exist.

## Target period — DETERMINISTIC

The target period is the most recently **fully-elapsed** unit before
invocation time (UTC). Never the current incomplete unit.

| Level | Target period = | Example (invoked 2026-05-25 03:00:00 UTC) |
| ----- | --------------- | ---------------------------------------- |
| day   | yesterday UTC               | 2026-05-24 (`20260524`) |
| week  | last completed ISO week     | 2026-W21 (May 18 – May 24) |
| month | last completed cal. month   | 2026-04 |

**Optional override arg**. Third positional arg overrides the
auto-computed period. Format must match the level:

```
/compact-memories episodes day 20260520
/compact-memories episodes week 2026-W18
/compact-memories episodes month 2026-03
/compact-memories diary week 2026-W18
/compact-memories diary month 2026-04
```

Use the override for backfills. Auto cron invocations omit the arg.

## Protocol

### 1. Compute target period

Use `date -u` and shell math; do NOT estimate. For ISO week, use
`date -u -d "<date>" +%G-W%V`.

### 2. Gather sources

**Episodes day**:
1. Glob `~/.claude/projects/*/*.jsonl`.
2. Keep files with mtime on or after the target date OR whose contents
   include lines from the target date (sessions spanning multiple days).
3. Parse JSONL, slice to the target date only.

**Episodes week**: glob `~/episodes/YYYYMMDD.md` for the 7 days of the
target ISO week (Monday–Sunday UTC).

**Episodes month**: glob `~/episodes/YYYY-W*.md` for ISO weeks whose
Monday falls within the target calendar month.

**Diary week**: glob `~/diary/YYYYMMDD.md` for the 7 days of the
target week.

**Diary month**: glob `~/diary/week/YYYY-W*.md` for the target month's
weeks.

### 3. Decide outcome

Three possible outcomes — log each to `~/episodes/.compact-log.jl`
(or `~/diary/.compact-log.jl` for diary store):

- **WRITE** — sources found, no existing output for this period (or
  override arg given). Proceed to compress + write.
- **SKIP_NO_SOURCES** — zero source files matched. Log and exit.
  Never write empty files.
- **SKIP_EXISTS** — output file already exists AND no override arg.
  Log and exit. Do NOT recompact (lossy + expensive). User must
  pass the period arg to force overwrite.

Log line shape (JSONL, one line per invocation):

```json
{"ts":"2026-05-25T03:00:42Z","level":"week","store":"episodes","target_period":"2026-W21","sources_found":7,"outcome":"WRITE","output":"~/episodes/2026-W21.md"}
{"ts":"2026-05-25T03:00:44Z","level":"week","store":"diary","target_period":"2026-W21","sources_found":0,"outcome":"SKIP_NO_SOURCES","output":null}
{"ts":"2026-05-25T03:00:45Z","level":"month","store":"diary","target_period":"2026-04","sources_found":4,"outcome":"WRITE","output":"~/diary/month/2026-04.md"}
```

This is the audit trail. `task_run_logs.status='success'` only
confirms the prompt was dispatched; the compact-log shows what
actually happened.

### 4. Compress (only on WRITE)

Preserve what the user corrected, not what the agent concluded. User
corrections are authoritative; conclusions get redrawn every recall.

Keep: user corrections (verbatim), preferences, accepted deliverables
(shipped/merged/confirmed), flagged blockers.

Drop: agent conclusions, routine ops (migrations, cron, /resolve calls),
dead-end debugging, unconfirmed inferences.

Each level is shorter than the sum of its sources.

### 5. Write (only on WRITE)

For diary-month: **first** `mkdir -p ~/diary/month/`. Directory may
not exist yet on first invocation per group.

Output file naming — STRICT:

| Level             | Path                          | Period format |
| ----------------- | ----------------------------- | ------------- |
| episodes day      | `~/episodes/YYYYMMDD.md`      | `YYYYMMDD`    |
| episodes week     | `~/episodes/YYYY-WNN.md`      | `YYYY-WNN`    |
| episodes month    | `~/episodes/YYYY-MM.md`       | `YYYY-MM`     |
| diary week        | `~/diary/week/YYYY-WNN.md`    | `YYYY-WNN`    |
| diary month       | `~/diary/month/YYYY-MM.md`    | `YYYY-MM`     |

Frontmatter — REQUIRED keys, strict format:

```yaml
---
summary: >
  - Shipped discord adapter (user confirmed)
  - Corrected: "sam is not stake-o-matic" (Apr 14)
period: '2026-W21'        # MUST match the level's period format above
type: week                # day | week | month
store: episodes           # episodes | diary
sources:                  # bare filenames only, no path prefix
  - 20260518.md
  - 20260519.md
  - 20260520.md
aggregated_at: '2026-05-25T03:00:42Z'
---
```

Validation rules (self-check before writing):
- `period` matches the level's required format
- `sources:` entries are bare filenames — no `episodes/` or `~/` prefix
- `sources:` has no duplicates
- `summary:` is non-empty
- No bullets/prose leak into the frontmatter — body content stays
  below the closing `---`

If a check fails: do NOT write. Log outcome as
`SKIP_VALIDATION_FAIL` with the failing rule.

## Usage

```
/compact-memories episodes day                  # cron — yesterday
/compact-memories episodes week                 # cron — last completed ISO week
/compact-memories episodes month                # cron — last completed cal. month
/compact-memories diary week                    # cron — last completed ISO week
/compact-memories diary month                   # cron — last completed cal. month

/compact-memories episodes day 20260520         # backfill specific day
/compact-memories episodes week 2026-W18        # backfill specific week
/compact-memories episodes month 2026-03        # backfill specific month
```

Override arg forces overwrite if output exists (still validates).

## Cron setup

On-demand. Check existing tasks first — don't duplicate.
All tasks use `contextMode: "isolated"`. `chat_jid` = group JID.

| Prompt                             | Cron        | When               |
| ---------------------------------- | ----------- | ------------------ |
| `/compact-memories episodes day`   | `0 2 * * *` | daily 02:00        |
| `/compact-memories episodes week`  | `0 3 * * 1` | Monday 03:00       |
| `/compact-memories episodes month` | `0 4 1 * *` | 1st of month 04:00 |
| `/compact-memories diary week`     | `0 3 * * 1` | Monday 03:00       |
| `/compact-memories diary month`    | `0 4 1 * *` | 1st of month 04:00 |

## Silent by convention

Cron compactions are SILENT — no chat output, no `<status>` blocks.
The artifact is the diff (file written or not). The compact-log JSONL
is the audit trail. Do NOT chat-emit "wrote X" or "no sources for Y."
