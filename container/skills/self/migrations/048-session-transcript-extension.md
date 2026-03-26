# Migration 048 — Session transcript extension: .jl → .jsonl

Claude Code writes session transcripts as `.jsonl` files, not `.jl`.
Any prior instructions saying `.jl` for session history were wrong.

## What changed

- `CLAUDE.md` session continuity section: glob now `*.jsonl`
- `self/SKILL.md` ls/Read examples: `*.jsonl`, `abc123.jsonl`
- `compact-memories/SKILL.md`: glob pattern `*.jsonl`

## What to do

No action needed — gateway already patched all deployed files. This
migration documents the correction for future reference.

If you still see `.jl` in any of these files, update them manually:

```bash
ls -t ~/.claude/projects/-home-node/*.jsonl | head -5
```
