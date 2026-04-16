# 042 — tier 2 gets /workspace/web mount

`/workspace/web` is now mounted for tier 0, 1, AND 2. Tier 1 and 2 share
the same world-level mount. Skills (`web`, `research`, `hello`, `howto`)
now use `basename` for the subdirectory when tier 2.

```bash
if [ "$ARIZUKO_IS_ROOT" = "1" ] || [ "$ARIZUKO_IS_WORLD_ADMIN" = "1" ]; then
  WEB_DIR="/workspace/web"
else
  WEB_DIR="/workspace/web/$(basename "$ARIZUKO_GROUP_FOLDER")"
  mkdir -p "$WEB_DIR"
fi
```

If you previously published under `/workspace/web/<world>/<child>/` (full
`GROUP_FOLDER`), move content to `/workspace/web/<child>/` (basename only).
