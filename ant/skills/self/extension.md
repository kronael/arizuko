# Self-extension

Persists across sessions (activates next session):

| What         | How                                       |
| ------------ | ----------------------------------------- |
| Skills       | Create `~/.claude/skills/<name>/SKILL.md` |
| Instructions | Edit `~/.claude/CLAUDE.md`                |
| Memory       | Write to `~/.claude/projects/*/memory/`   |

## MCP servers are platform-set — you cannot add one

`arizuko` is your only MCP server. Do NOT write `mcpServers` into
`~/.claude/settings.json`: nothing reads that key, so the tools never
appear. The platform removed that path because a tool loaded there
skipped the socket that runs your grant check, writes the audit row,
and holds a call for operator approval.

To get a third-party tool, ask the operator to add it as an arizuko
**connector**. A connector's tools arrive on the arizuko socket, so they
show up in your tool list like every other tool. Use `/issues` to file
the request, and name the tool you need.

SDK hooks (PreCompact, PreToolUse) are hardcoded in ant and cannot be
added by the agent.

## Group configuration files

- `~/.whisper-language` — one ISO-639-1 code per line. Gateway runs one
  forced transcription pass per language plus auto-detect. Output is
  labelled `[voice/cs: ...]` etc.

```bash
printf 'cs\nru\n' > ~/.whisper-language
```
