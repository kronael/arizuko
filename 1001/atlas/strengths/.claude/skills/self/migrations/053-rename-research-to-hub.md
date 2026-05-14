# 053 — rename /research skill to /hub

The agent-side skill formerly at `~/.claude/skills/research/` is
renamed to `~/.claude/skills/hub/` to avoid clashing with Claude's
built-in `research` tool, which the LLM was shadowing.

## Check

```bash
[ -d ~/.claude/skills/hub ] && [ ! -d ~/.claude/skills/research ]
```

## Steps

```bash
if [ -d ~/.claude/skills/research ]; then
  rm -rf ~/.claude/skills/research
fi
```

## After

The image's COPY layer already seeds `~/.claude/skills/hub/` from
`ant/skills/hub/`. This migration only cleans up stale `research/`
directories in per-group overlays.

Bump `MIGRATION_VERSION` to 53.
