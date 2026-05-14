# 011 — Routing and delegation tools

## Goal

Update SKILL.md MCP tools table to include `refresh_groups`,
`delegate_group`, `set_routing_rules`, and `reset_session`.
Remove phantom `list_tasks` entry.

## Check

```bash
grep -q 'delegate_group' ~/.claude/skills/self/SKILL.md && exit 0
```

## Steps

```bash
cp /workspace/self/container/skills/self/SKILL.md ~/.claude/skills/self/SKILL.md
```

## After

```bash
echo "11" > ~/.claude/skills/self/MIGRATION_VERSION
```
