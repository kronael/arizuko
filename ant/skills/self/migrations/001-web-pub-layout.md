# 001 — web/pub/ layout

Move `web/` root files into `web/pub/`.

```bash
test -f /workspace/web/pub/index.html && echo skip && exit 0
test ! -f /workspace/web/index.html  && echo skip && exit 0

mkdir -p /workspace/web/pub/howto /workspace/web/pub/assets /workspace/web/priv
mv /workspace/web/index.html       /workspace/web/pub/index.html       2>/dev/null
mv /workspace/web/howto/index.html /workspace/web/pub/howto/index.html 2>/dev/null
mv /workspace/web/assets/hub.css   /workspace/web/pub/assets/hub.css   2>/dev/null
mv /workspace/web/assets/hub.js    /workspace/web/pub/assets/hub.js    2>/dev/null

sed -i 's|/assets/|/pub/assets/|g; s|href="/howto/"|href="/pub/howto/"|g' \
  /workspace/web/pub/index.html 2>/dev/null

echo pub-v1 > /workspace/web/.layout
kill $(cat /srv/app/tmp/vite.pid) 2>/dev/null || true
```
