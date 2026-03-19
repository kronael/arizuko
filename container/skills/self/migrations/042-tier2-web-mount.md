---
version: 42
description: tier 2 now has /workspace/web mount; fix web path logic
---

## What changed

- `/workspace/web` is now mounted for tier 0, 1, AND 2 groups
- Tier 0 (root): full web root
- Tier 1 (world admin): `web/<world>/` mounted as `/workspace/web`
- Tier 2 (child): `web/<world>/` mounted as `/workspace/web` (same mount)
- Skills updated: web, research, hello, howto — all now use `basename`
  for the subdirectory when tier 2, not the full `$GROUP_FOLDER`

## Convention (updated)

```bash
GROUP_FOLDER=$NANOCLAW_GROUP_FOLDER
if [ "$NANOCLAW_IS_ROOT" = "1" ] || [ "$NANOCLAW_IS_WORLD_ADMIN" = "1" ]; then
  WEB_DIR="/workspace/web"
else
  WEB_SUB=$(basename "$GROUP_FOLDER")
  WEB_DIR="/workspace/web/$WEB_SUB"
  mkdir -p "$WEB_DIR"
fi
```

## Action required

Check if `/workspace/web` is now available: `ls /workspace/web/ 2>/dev/null && echo ok`

If you previously published content and it's not visible, check the path:

- Old path: `/workspace/web/atlas/support/` (wrong — used full GROUP_FOLDER)
- New path: `/workspace/web/support/` (correct — use basename only)

Move content if needed:

```bash
# Only if /workspace/web exists and you're tier 2
if [ "$NANOCLAW_TIER" = "2" ]; then
  WEB_SUB=$(basename "$NANOCLAW_GROUP_FOLDER")
  mkdir -p "/workspace/web/$WEB_SUB"
fi
```
