# 046 — send_file: home dir is group dir

## Goal

The agent home dir (~/) is the group directory, shared with the gateway.
Files saved under ~/ are accessible to send_file. Use ~/tmp/ for temp files.
Fix any /workspace/group references in CLAUDE.md introduced by a wrong migration.

## Check

```bash
grep -q "/workspace/group" ~/.claude/CLAUDE.md && echo "needs fix" || echo "ok"
```

## Steps

```bash
sed -i 's|/workspace/group/tmp|~/tmp|g' ~/.claude/CLAUDE.md
sed -i 's|under /workspace/group/ or /workspace/media/|under ~/|g' ~/.claude/CLAUDE.md
mkdir -p ~/tmp
```

## After

```bash
echo "46" > ~/.claude/skills/self/MIGRATION_VERSION
```
