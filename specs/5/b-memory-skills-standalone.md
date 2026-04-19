---
status: unshipped
---

# Memory skills — standalone distribution

Extract the memory system (skills + prompt-time injection) for use
outside arizuko.

Two layers:

- **Portable** — `diary`, `facts`, `recall-memories`, `compact-memories`,
  `users` SKILL.md files + memory-routing CLAUDE.md snippet. Plain
  markdown, loaded by Claude Code natively. No arizuko deps.
- **Not portable without replacement** — `recall-messages` (needs
  `get_history` IPC, see [C-message-mcp.md](C-message-mcp.md)),
  `schedule_task` MCP (needs `timed`), gateway injection of
  diary/episodes/user summaries (reimplement in ~100 LOC each).

Distribution: single git repo `memory-skills/` with `skills/`,
`CLAUDE-memory-section.md`, `inject/{diary,episodes,router}.go`
extracted. No npm, no build. Users copy to `~/.claude/skills/` and
paste the CLAUDE.md snippet.

Rationale: the memory layer has zero runtime deps; easy to share
without the full router.

Open questions:

1. `get_history` IPC: implement in arizuko, or document
   `recall-messages` as arizuko-only?
2. `recall` binary (v2 FTS5+vector) not in repo/Dockerfile; fall-back
   grep path is what runs today. Keep v2 path in skill or remove?
3. `compact-memories` hardcodes `.claude/projects/-home-node/*.jsonl`.
   Portable path: `$(ls -t ~/.claude/projects/*/*.jsonl | head -1 |
xargs dirname)`.
4. `schedule_task` parameter mismatch: skill uses `targetFolder`/
   `schedule_type`/`schedule_value`; MCP takes `targetJid`/`cron`/
   `contextMode`. Verify.
5. License: AGPL on prompt text (not code) is awkward. Decide
   CC0/MIT for the skill files before public release.
