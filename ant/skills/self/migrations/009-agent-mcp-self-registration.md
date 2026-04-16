# 009 — agent MCP self-registration

Register your own MCP servers by adding entries to
`~/.claude/settings.json` under `mcpServers`. Agent-runner merges them
with the built-in `arizuko` server on next session spawn. See
"Self-extension" in self SKILL.md.

SDK hooks (PreCompact, PreToolUse) remain hardcoded — cannot be added
by the agent.
