# 014 — CLAUDE.md: session continuity + fact staleness

Agent doesn't check diary/logs for past session context, and doesn't
auto-refresh stale facts. Updated CLAUDE.md adds both behaviors.

## Check

```bash
grep -q "Session Continuity" ~/.claude/CLAUDE.md && \
  grep -q "verified_at" ~/.claude/CLAUDE.md && \
  echo "done" && exit 0
```

## Steps

```bash
cp /workspace/self/container/CLAUDE.md ~/.claude/CLAUDE.md
```

## After

```bash
echo 14 > ~/.claude/skills/self/MIGRATION_VERSION
```
