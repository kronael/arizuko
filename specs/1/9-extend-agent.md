---
status: shipped
---

# Agent Self-Extension

How the agent extends itself across sessions. Runs Claude
Code inside a container with persistent `~/.claude/`.

## SDK-native mechanisms (shipped)

| Mechanism    | Path                               | Effect                  |
| ------------ | ---------------------------------- | ----------------------- |
| Skills       | `~/.claude/skills/<name>/SKILL.md` | New capabilities        |
| Instructions | `~/.claude/CLAUDE.md`              | Behavior/personality    |
| Memory       | `~/.claude/projects/*/memory/`     | Cross-session knowledge |
| Settings     | `~/.claude/settings.json`          | SDK configuration       |

Changes take effect on next session spawn. Skills seeded
from `container/skills/`. See `extend-skills.md`.

## Agent-registered MCP servers (v1 -- to ship)

Agent registers MCP servers via `~/.claude/settings.json`.
Agent-runner merges with built-in `nanoclaw` server.

1. Agent writes MCP server binary to workspace
2. Agent adds to `settings.json`:

```json
{
  "mcpServers": {
    "mytools": {
      "command": "node",
      "args": ["/home/node/tools/myserver.js"]
    }
  }
}
```

3. Next spawn: agent-runner reads, merges MCP servers
4. Agent has `mcp__mytools__*` tools

### Agent-runner changes

**Merge MCP servers** (built-in `nanoclaw` wins):

```typescript
const mcpServers = { ...agentMcp, nanoclaw: { ... } };
```

**Dynamic allowedTools**:

```typescript
const mcpWildcards = Object.keys(mcpServers).map((name) => `mcp__${name}__*`);
const allowedTools = [...builtinTools, ...mcpWildcards];
```

### Security

- Agent can only run binaries inside container (sandboxed)
- `nanoclaw` cannot be overridden (spread order)
- Agent MCP servers are container-local, no gateway access
- For isolated MCP servers (own containers), see phase 3 specs

### Gateway settings preservation

Gateway writes to `settings.json` per-spawn for env
injection. Must preserve agent-written `mcpServers`.

## Agent-runner hot-patching

Agent-runner source (`container/agent-runner/src/`) is mounted
into the container at `/app/src`. The entrypoint recompiles
with `tsc` on every spawn to pick up agent self-modifications.

Agent can modify its own runner code; changes take effect on
next spawn after recompilation.

**Build pipeline**: `make build` compiles agent-runner locally.
`make lint` and pre-commit typecheck it. The Dockerfile also
compiles during image build. Runtime tsc is only for hot-patched
source — the image ships pre-compiled JS as fallback.

## Known limitation: hooks

Agent cannot add SDK hooks (PreCompact, PreToolUse, etc).
Hardcoded in agent-runner. No agent-facing mechanism for v1.

## What the agent cannot extend

Gateway-side (requires developer changes):
actions, channels, MIME handlers, volume mounts,
inbound pipeline. See `extend-gateway.md`.
