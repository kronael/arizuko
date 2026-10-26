# Self-extension

Persists across sessions (activates next session):

| What         | How                                       |
| ------------ | ----------------------------------------- |
| Skills       | Create `~/.claude/skills/<name>/SKILL.md` |
| Instructions | Edit `~/CLAUDE.md`                       |
| Memory       | Write to `~/.claude/projects/*/memory/`   |

Core and selected packages supply read-only skills at `~/.claude/skills/`.
The core manual mounts at `~/.claude/CLAUDE.md`.
ALWAYS keep group instructions in `~/CLAUDE.md` and custom skills under a distinct name.
ALWAYS use the dashboard toggle or `~/.claude/skills/<name>/.disabled` to disable a packaged skill.

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

- `~/.gateway-caps` — read-only TOML the platform rewrites before every
  turn. It says what the instance has enabled, so you can tell before you
  try:

  ```toml
  [voice]
  enabled = true
  model = "turbo"
  [video]
  enabled = false
  [media]
  enabled = true
  max_size_mb = 20
  [web]
  enabled = true
  host = "example.com"
  ```

  `[voice]` is inbound transcription, `[media]` is attachment download with
  its size cap, `[web]` names the public host when one is set. The file is
  advisory: a tool call still meets its own grant check.

- `~/.whisper-language` — one ISO-639-1 code per line. Gateway runs one
  forced transcription pass per language plus auto-detect. Output is
  labelled `[voice/cs: ...]` etc.

```bash
printf 'cs\nru\n' > ~/.whisper-language
```
