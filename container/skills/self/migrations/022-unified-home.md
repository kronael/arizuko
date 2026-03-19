# 022 — Unified home directory

## What changed

The agent's working directory is now `/home/node/` (was `/workspace/group/`).
The group folder is mounted directly as the agent's home directory.

- `cwd` is `/home/node/` (was `/workspace/group/`)
- `SOUL.md` lives at `/home/node/SOUL.md` (was `~/.claude/SOUL.md`)
- `.claude/` lives at `/home/node/.claude/` (same as `~/.claude/`)
- Session transcripts: `~/.claude/projects/-home-node/` (was `-workspace-group`)
- `/workspace/group` no longer exists

## What to do

- Scripts using `basename /workspace/group` should use `$NANOCLAW_GROUP_FOLDER`
- Scripts using `/workspace/group/` paths should use relative paths or `/home/node/`
- SOUL.md references: `~/.claude/SOUL.md` → just `SOUL.md`
- Root migrate: `/workspace/data/sessions/` → `~/groups/`

No action required — paths are updated in skills automatically via migrate.
