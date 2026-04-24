# diary

Diary annotation reader + recovery writer.

## Purpose

Reads recent diary entries from `groups/<folder>/diary/` and returns them
as an XML `<knowledge layer="diary">` block for prompt injection.
Extracts `summary:` frontmatter and labels entries by age (today,
yesterday, N days/weeks ago).

## Public API

- `Read(groupDir string, max int) string` — XML block for the prompt
- `WriteRecovery(groupDir, reason, errMsg string)` — write a recovery entry after crash/timeout
- `ExtractSummary(path string) string` — parse frontmatter `summary:`

## Dependencies

None (stdlib only).

## Files

- `diary.go`

## Related docs

- `ARCHITECTURE.md` (Diary)
- `specs/1/L-memory-diary.md`
- `specs/4/24-recall.md`
