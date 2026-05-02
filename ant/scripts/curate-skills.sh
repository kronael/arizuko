#!/bin/bash
# curate-skills.sh — partition ant/skills/ into portable vs arizuko-only.
#
# Run from the repo root. A skill is "arizuko-only" if its SKILL.md
# mentions @gated, @arizuko, or gated.sock; otherwise it is portable
# (loadable by Claude Code without the rest of arizuko).
#
# Used as the gate for the skill-curation phase of the standalone-ant
# spec (specs/5/b-ant-standalone.md). Re-run after every skill edit.
set -euo pipefail

for d in ant/skills/*/; do
    if grep -lE '@gated|@arizuko|gated\.sock' "$d/SKILL.md" >/dev/null 2>&1; then
        echo "ARIZUKO-ONLY: $d"
    else
        echo "PORTABLE:    $d"
    fi
done
