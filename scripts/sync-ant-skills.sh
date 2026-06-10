#!/usr/bin/env bash
# Refresh the agent skills that arizuko vendors from kronael/tools.
#
# PROTOCOL — refreshing the agent skill bundle to a new tools version:
#   1. Update the tools checkout (sibling repo): cd ../tools && git pull
#   2. Run this script — rsyncs the vendored skills below into ant/skills/.
#   3. Review the diff; commit `[skills] sync from kronael/tools`.
#   4. Deliver to agents: bump ant/skills/self/MIGRATION_VERSION, add a
#      migrations/<N>-vX.Y.Z-*.md file (the broadcast text), update
#      ant/skills/self/migration.md "Latest migration version", then
#      `sudo make -C ant image`.
#   5. Fire the migrate: routd reads MIGRATION_VERSION at startup
#      (checkMigrationVersion, loop.go) and enqueues /migrate for behind
#      root groups → restart each instance so its routd re-reads the bump.
#
# Only these skills are vendored from tools; everything else in ant/skills/
# is arizuko-owned — NEVER bulk-copy all of tools/skills (it would clobber
# arizuko-tuned skills of the same name, e.g. diary/go/oracle/specs).

set -euo pipefail
cd "$(dirname "$0")/.."

TOOLS="${1:-../tools}/skills"
[ -d "$TOOLS" ] || { echo "Tools skills not found at $TOOLS — pass the path: $0 /path/to/tools" >&2; exit 1; }

VENDORED="browse sonnet haiku
create-architecture-diagram create-ascii-art create-ascii-video
create-claude-design create-design-md create-excalidraw create-humanizer
create-manim-video create-p5js create-popular-web-designs create-pretext
create-sketch create-video-render create-video-script"

for s in $VENDORED; do
  if [ -d "$TOOLS/$s" ]; then
    rsync -a --delete "$TOOLS/$s/" "ant/skills/$s/"
    echo "synced $s"
  else
    echo "WARN: $s not in $TOOLS (skipped)" >&2
  fi
done

echo "done — review 'git status ant/skills/', then commit + bump MIGRATION_VERSION (see header)."
