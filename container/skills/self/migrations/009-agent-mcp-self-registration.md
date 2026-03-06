# 009 — Agent MCP self-registration

You can now register your own MCP servers by adding entries to
`~/.claude/settings.json` under `mcpServers`. On next session spawn,
agent-runner merges them with the built-in `nanoclaw` server and
makes their tools available.

See the "Self-extension" section in `~/.claude/skills/self/SKILL.md`
for usage instructions.

Known limitation: SDK hooks (PreCompact, PreToolUse) cannot be
added by the agent — these remain hardcoded in agent-runner.
