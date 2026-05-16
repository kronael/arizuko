# Self-extension

Persists across sessions (activates next session):

| What         | How                                           |
| ------------ | --------------------------------------------- |
| Skills       | Create `~/.claude/skills/<name>/SKILL.md`     |
| Instructions | Edit `~/.claude/CLAUDE.md`                    |
| Memory       | Write to `~/.claude/projects/*/memory/`       |
| MCP servers  | Add to `~/.claude/settings.json` `mcpServers` |

## Registering MCP servers

```bash
cat > ~/tools/myserver.js << 'EOF'
// ... your MCP server implementation ...
EOF

node -e "
const f = process.env.HOME + '/.claude/settings.json';
const s = JSON.parse(require('fs').readFileSync(f, 'utf-8'));
s.mcpServers = s.mcpServers || {};
s.mcpServers.mytools = { command: 'node', args: [process.env.HOME + '/tools/myserver.js'] };
require('fs').writeFileSync(f, JSON.stringify(s, null, 2) + '\n');
"
```

Tools appear as `mcp__mytools__*` next session. The built-in `arizuko`
server cannot be overridden. SDK hooks (PreCompact, PreToolUse) are
hardcoded in ant and cannot be added by the agent.

## Group configuration files

- `~/.whisper-language` — one ISO-639-1 code per line. Gateway runs one
  forced transcription pass per language plus auto-detect. Output is
  labelled `[voice/cs: ...]` etc.

```bash
printf 'cs\nru\n' > ~/.whisper-language
```
