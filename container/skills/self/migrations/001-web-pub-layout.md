# Migration 001: web/pub/ layout

**Goal**: move web/ root files into web/pub/ per the new layout convention.

## Check (skip if already done)

```bash
# Already migrated if pub/index.html exists
test -f /workspace/web/pub/index.html && echo "already migrated — skip" && exit 0

# Nothing to migrate if old index.html absent either
test ! -f /workspace/web/index.html && echo "no legacy layout — skip" && exit 0
```

## Steps

```bash
mkdir -p /workspace/web/pub/howto /workspace/web/pub/assets /workspace/web/priv

# Move root files
test -f /workspace/web/index.html && mv /workspace/web/index.html /workspace/web/pub/index.html
test -f /workspace/web/howto/index.html && mv /workspace/web/howto/index.html /workspace/web/pub/howto/index.html
test -f /workspace/web/assets/hub.css && mv /workspace/web/assets/hub.css /workspace/web/pub/assets/hub.css
test -f /workspace/web/assets/hub.js && mv /workspace/web/assets/hub.js /workspace/web/pub/assets/hub.js

# Fix asset paths in hub page
test -f /workspace/web/pub/index.html && \
  sed -i 's|/assets/|/pub/assets/|g' /workspace/web/pub/index.html
test -f /workspace/web/pub/index.html && \
  sed -i 's|href="/howto/"|href="/pub/howto/"|g' /workspace/web/pub/index.html

# Mark layout version
echo "pub-v1" > /workspace/web/.layout

# Restart vite
kill $(cat /srv/app/tmp/vite.pid) 2>/dev/null || true
sleep 2

# Verify
curl -sf "http://localhost:${VITE_PORT:-5173}/pub/" | grep -q hub \
  && echo "migration 001 OK" \
  || echo "WARNING: /pub/ not responding as expected — check vite"
```

## After

```bash
echo "1" > ~/.claude/skills/self/MIGRATION_VERSION
```
