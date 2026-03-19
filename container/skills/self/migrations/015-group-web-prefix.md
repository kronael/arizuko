# 015 — Group web prefix convention

Skills that publish web content now use a group-specific prefix
within `/workspace/web/` instead of hardcoded paths.

## What changed

- **hello**: detects group folder, links howto at group web prefix
- **howto**: deploys to `$WEB_DIR/howto/` where `WEB_DIR` is
  `/workspace/web/$GROUP_FOLDER` for non-root groups
- **web**: documents the `WEB_DIR` convention, group isolation
- **research**: deploys research hubs under group prefix

## Convention

```bash
GROUP_FOLDER=$(basename /workspace/group)
if [ "$NANOCLAW_IS_ROOT" = "1" ]; then
  WEB_DIR="/workspace/web"
else
  WEB_DIR="/workspace/web/$GROUP_FOLDER"
fi
```

Root groups publish at web root. Non-root groups publish under
their folder name.

## Three-level howto

The howto skill now generates content in three sections:

1. **Beginner** — research, shopping, web browsing, email
2. **Intermediate** — building web apps, dashboards, data viz
3. **Advanced** — groups, routing, scheduled tasks, MCP

## Migration steps

1. Re-read updated skills: `/hello`, `/howto`, `/web`
2. If group has existing howto at `/workspace/web/pub/howto/`,
   move to group prefix: `$WEB_DIR/howto/`
3. Regenerate howto with three-level structure if desired
