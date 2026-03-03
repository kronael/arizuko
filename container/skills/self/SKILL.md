---
name: self
description: Introspect this agent instance — version, layout, skills, channels, pending migrations. Use when asked "who are you", "introspect", "what version", "status", or "what's installed".
---

# Self

Introspect this agent instance.

## All groups: introspect

```bash
# Layout convention
cat /web/.layout 2>/dev/null || echo legacy

# Skills installed
ls ~/.claude/skills/

# Channels enabled (check env)
env | grep -E '(TELEGRAM_BOT_TOKEN|DISCORD_BOT_TOKEN)' | sed 's/=.*/=<set>/'

# Web apps deployed
ls /web/

# Migration version
cat ~/.claude/skills/self/MIGRATION_VERSION 2>/dev/null || echo 0
```

Latest migration version: **1**

If `MIGRATION_VERSION` < 1: migrations are pending. Tell the user.

## Main group only

Main group has `/workspace/project/` mounted (no `/workspace/global/`).
Check: `test ! -d /workspace/global && echo main`

```bash
# Source layout
ls /workspace/project/

# Changelog
cat /workspace/project/CHANGELOG.md

# Recent git log
git -C /workspace/project log --oneline -10

# Pending migrations
current=$(cat ~/.claude/skills/self/MIGRATION_VERSION 2>/dev/null || echo 0)
latest=$(ls ~/.claude/skills/self/migrations/*.md 2>/dev/null \
  | grep -oP '^\d+' | sort -n | tail -1 || echo 0)
echo "migration: $current / $latest"
```

If migrations pending or skills stale: suggest running `/migrate`.
