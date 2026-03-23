---
version: 047
description: web path convention — check for /workspace/web/$GROUP_FOLDER nesting error
---

# Migration 047 — web-dir-convention

The `/workspace/web` mount is already group-scoped by the gateway:

- Tier 0 (root): full `/workspace/web` root
- Tier 1 (world): `/workspace/web/<world>/` mounted as `/workspace/web`
- Tier 2+ (child): same as tier 1

Writing to `/workspace/web/$ARIZUKO_GROUP_FOLDER/` nests content one level too
deep and makes it inaccessible via the web mount.

## Check

```bash
if [ -d "/workspace/web" ]; then
  GROUP_SUB=$(basename "$ARIZUKO_GROUP_FOLDER")
  if [ -d "/workspace/web/$GROUP_SUB" ]; then
    echo "misplaced content found at /workspace/web/$GROUP_SUB — review and move to /workspace/web/"
  else
    echo "ok"
  fi
fi
```

## Fix (if needed)

```bash
GROUP_SUB=$(basename "$ARIZUKO_GROUP_FOLDER")
if [ -d "/workspace/web/$GROUP_SUB" ]; then
  mv /workspace/web/$GROUP_SUB/* /workspace/web/ 2>/dev/null && rmdir /workspace/web/$GROUP_SUB
fi
```

## After

```bash
echo "47" > ~/.claude/skills/self/MIGRATION_VERSION
```
