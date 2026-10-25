#!/usr/bin/env bash
# Refresh the skills arizuko vendors from kronael/tools into ant/skills/.
#
# Run: make -C ant sync-skills   (TOOLS=/path/to/tools to override the default)
#
# THE ALLOW-LIST BELOW IS THE POINT. `ant/skills` and `tools/skills` share 49
# names, and only the ones named here may be delete-synced. Every other shared
# name carries arizuko tuning that a wholesale copy would destroy — the tuning
# is invisible in a directory listing, which is why it is written down instead
# of rediscovered by diffing each time.
#
# Three groups, measured 2026-08-28 against tools da28b93:
#
#   VENDORED (below) — byte-identical to upstream, so arizuko has made no local
#   change and a delete-sync is safe. 15 skills.
#
#   TUNED — arizuko rewrote the frontmatter, the body, or both. NEVER sync:
#     agent-browser bugs cli commit create data diary dispatch distill go
#     improve learn ops oracle readme recall-memories refine release
#     resolve service ship specs sql trader tweet visual wisdom
#   The common shape is a `description:` rewritten into arizuko's multi-line
#   "/x — launch @x agent … USE for … NOT for …" form, plus arizuko-only body
#   sections (distill's context-reducer comparison, specs' ship discipline).
#
#   NOT SHIPPED: the upstream review, PR-comment and PR-draft skills. Their
#   text is owner-host text (push, merge and PR bans, a sign-off prompt, owner
#   hooks). The agent's GitHub conduct is output-styles/github.md alone.
#
#   UPSTREAM-AHEAD — upstream added real content AND arizuko tuned the same
#   file, so these are a per-skill MERGE, never a sync: py rs software ts.
#   Upstream carries `## Lints` sections plus `lints/` ast-grep rule dirs
#   (~100K of rules + fixtures driven by the tools repo's own `make lints`) and
#   `software/js-perf.md`. The lint dirs have no consumer inside an agent
#   container; js-perf.md and ts's `## Performance` section do. Adopt by hand.
#
# After a sync that changes anything, deliver it (the step that gets forgotten):
#   1. bump ant/skills/self/MIGRATION_VERSION
#   2. add ant/skills/self/migrations/<N>-vX.Y.Z-<topic>.md (its first lines are
#      the chat broadcast) and update migration.md "Latest migration version"
#   3. sudo make -C ant image
#   4. restart each instance so routd re-reads the version, then VERIFY a
#      group's stored version advanced and the skills landed in ~/.claude/skills
set -euo pipefail

TOOLS="${TOOLS:-$HOME/app/tools}"
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$TOOLS/skills"
DST="$HERE/skills"

VENDORED=(
  browse create-eval diagrams explore fable fin haiku humanize
  merge opus scavenge sh sonnet tsx writing
)

[ -d "$SRC" ] || { echo "no tools skills at $SRC (set TOOLS=)" >&2; exit 1; }

if [ -n "$(git -C "$TOOLS" status --porcelain skills 2>/dev/null)" ]; then
  echo "warning: $TOOLS/skills has uncommitted changes; they will be synced" >&2
fi

for s in "${VENDORED[@]}"; do
  [ -d "$SRC/$s" ] || { echo "missing upstream skill: $s" >&2; exit 1; }
  rsync -a --delete "$SRC/$s/" "$DST/$s/"
done

echo "synced ${#VENDORED[@]} vendored skills from $SRC"
git -C "$HERE/.." diff --stat -- ant/skills || true
echo
echo "no diff above = the bundle was already current; nothing to deliver."
