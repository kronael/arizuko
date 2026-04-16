# 053 — rename /research skill to /hub

The skill at `~/.claude/skills/research/` is renamed to
`~/.claude/skills/hub/` to avoid shadowing Claude's built-in `research`
tool. The image seeds `hub/`; remove the stale dir:

```bash
rm -rf ~/.claude/skills/research
```
