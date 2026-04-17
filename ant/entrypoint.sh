#!/bin/bash
# Arizuko agent entrypoint.
# Compiles TypeScript to a per-run /tmp/dist and runs index.js on stdin JSON.
# Secrets arrive via stdin, are written to /tmp/input.json, and deleted once
# Node has read them. Follow-up messages arrive via /workspace/ipc/input/.
set -e
cd /app && bunx tsc --outDir /tmp/dist 2>&1 >&2
ln -s /app/node_modules /tmp/dist/node_modules
chmod -R a-w /tmp/dist
cat > /tmp/input.json
node /tmp/dist/index.js < /tmp/input.json
