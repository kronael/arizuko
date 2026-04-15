---
name: commit
description: >
  Stage and commit changes to git with proper [section] message
  format. Use when asked to commit or after completing work.
user-invocable: true
---

# Commit

## When

- `/commit`: ALWAYS proceed
- Auto (hook): only if cohesive chunk (single fix/feat/refactor, related files, complete work)
- Not cohesive: report and stop

## Format

`[section] Message` — why not what, 1-2 sentences.
Sections: fix, feat, refactor, docs, test, chore, perf, style

Markers: `[checkpoint]` -> `[checkpoint] Message`, `[refined]` -> `[section] Message [refined]`

## Workflow

1. `git status` + `git diff` + `git log --oneline -5`
2. Decide commit or not
3. Draft message
4. Commit directly: `git commit -m "msg" -- file1 file2`
5. If pre-commit reformats, retry once
6. If index.lock: `rm -f .git/index.lock`, retry once

## Rules

- ALWAYS `git commit -m "msg" -- file1 file2` (direct, no staging)
- ALWAYS commit whole files, list each explicitly
- NEVER `git add` (commit directly with -- pathspec)
- NEVER `git commit` without `-- file1 file2`
- NEVER `git commit --amend`
- NEVER `git commit -a`
- NEVER `git stash`
- NEVER Co-Authored-By
- NEVER skip pre-commit hooks
- Ignore other agents' uncommitted changes
