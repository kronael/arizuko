<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Agent Self-Extension

Agent extends itself across sessions via Claude Code inside a
container with persistent `~/.claude/`.

## SDK-native mechanisms

| Mechanism    | Path                               | Effect                  |
| ------------ | ---------------------------------- | ----------------------- |
| Skills       | `~/.claude/skills/<name>/SKILL.md` | New capabilities        |
| Instructions | `~/.claude/CLAUDE.md`              | Behavior/personality    |
| Memory       | `~/.claude/projects/*/memory/`     | Cross-session knowledge |
| Settings     | `~/.claude/settings.json`          | SDK configuration       |

Changes take effect on next session spawn.

## settings.json merge order

Agent-written servers < sidecar servers < arizuko (arizuko always wins).

Agent registers MCP servers via `settings.json`. Agent-runner merges
with built-in `arizuko` server. Agent MCP servers are container-local,
no gateway access.

## Known limitation: hooks

Agent cannot add SDK hooks (PreCompact, PreToolUse, etc). Hardcoded
in agent-runner. No agent-facing mechanism for v1.

## What the agent cannot extend

Gateway-side (requires developer changes): actions, channels,
MIME handlers, volume mounts, inbound pipeline.
