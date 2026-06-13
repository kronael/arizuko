# 161 — migrate skill: run-discipline nudge + self-heal

The `/migrate` skill gained two robustness sections (delivered via the
mounted `/opt/arizuko`, refreshed every spawn by `seedMigrateSkill`):

- **Run discipline** — do NOT bail on a stale "/migrate blocked" /
  "`/workspace` not mounted" memory. The source is `/opt/arizuko/ant/`
  (FHS rename, v0.45.11); verify the live layout THIS TURN
  (`ls /opt/arizuko/ant/skills`) before concluding anything. Cost an
  11-day skill freeze on marinade/atlas where the agent re-ran
  `ls /workspace` from session memory instead of reading the refreshed
  skill (fixed in tandem by routd clearing the session before /migrate).

- **Self-heal** — a group predating `.merge-base/` (or a whole skill)
  has no merge base; that is "first sync" (copy `theirs → ours`, create
  base), NOT a failure. After a run, every stock skill must exist under
  `~/.claude/skills/` unless `.disabled`.

No action required — this migration documents the skill change; the
auto-migrate path delivers and runs it.
