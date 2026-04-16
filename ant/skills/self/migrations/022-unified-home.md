# 022 — unified home directory

Agent cwd is now `/home/node/` (was `/workspace/group/`). The group
folder is mounted directly as the agent's home. `/workspace/group/` no
longer exists.

- `SOUL.md` at `/home/node/SOUL.md`
- Session transcripts at `~/.claude/projects/-home-node/`
- Root sees all group sessions at `/workspace/data/groups/`

Scripts using `basename /workspace/group` should use `$ARIZUKO_GROUP_FOLDER`;
other `/workspace/group/` paths become `~` or relative.
