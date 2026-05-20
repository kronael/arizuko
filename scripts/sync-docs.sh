#!/bin/bash
set -euo pipefail

INSTANCE=${1:-krons}
SRC=/home/onvos/app/arizuko/template/web/pub/
DST=/srv/data/arizuko_${INSTANCE}/web/pub/arizuko/

sudo rsync -a --delete "$SRC" "$DST"
echo "synced to $INSTANCE"
curl -s -o /dev/null -w "verify: %{http_code}\n" "https://${INSTANCE}.fiu.wtf/pub/arizuko/"
