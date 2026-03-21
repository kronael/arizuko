# 046 — send_file: correct workspace path

## Goal

Fix the workspace path for `send_file`. Only `/workspace/group/...` and
`/workspace/media/...` are shared with the gateway and accessible for
file delivery. `~/` (`/home/node/`) is in-container only and rejected.

## Check

```bash
grep -q "/workspace/group/tmp" ~/.claude/CLAUDE.md && echo "done"
```

## Steps

Update `~/.claude/CLAUDE.md` — replace any `~/tmp` or `send_file` path
guidance referencing `~` with the correct path:

```bash
sed -i 's|~/tmp|/workspace/group/tmp|g' ~/.claude/CLAUDE.md
sed -i 's|under ~/|under /workspace/group/ or /workspace/media/|g' ~/.claude/CLAUDE.md
```

Create the tmp dir if missing:

```bash
mkdir -p /workspace/group/tmp
```

## After

```bash
echo "46" > ~/.claude/skills/self/MIGRATION_VERSION
```
