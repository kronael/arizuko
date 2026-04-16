# 015 — group web prefix

Non-root groups publish under `/workspace/web/<folder>/`; root at
`/workspace/web/`. Skills updated: `hello`, `howto`, `web`, `research`.

```bash
if [ "$ARIZUKO_IS_ROOT" = "1" ]; then
  WEB_DIR="/workspace/web"
else
  WEB_DIR="/workspace/web/$(basename /workspace/group)"
fi
```

If your group has an existing howto at `/workspace/web/pub/howto/`,
move it to `$WEB_DIR/howto/`.
