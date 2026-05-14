# 021 — SOUL.md now at ~/.claude/

Gateway copies group `SOUL.md` to `~/.claude/SOUL.md` at every spawn.
Agent-runner checks `~/.claude/SOUL.md` instead of `/workspace/group/SOUL.md`.

No agent-side action needed — the gateway handles the copy automatically.
If you have code that reads `/workspace/group/SOUL.md`, update the path
to `~/.claude/SOUL.md`.
