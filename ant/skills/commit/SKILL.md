---
name: commit
description: >
  Stage and commit with `[section] Message` format. USE when asked to
  commit, after completing a cohesive chunk of work, on hook auto-commit
  trigger. NOT for PRs (use pr-draft) or changes spanning unrelated
  concerns (split first).
user-invocable: true
---

# Commit

## Format

`[section] Message` — why not what, 1-2 sentences.
Sections: fix, feat, refactor, docs, test, chore, perf, style

Markers: `[checkpoint]`, `[section] ... [refined]`

## Workflow

1. `git status` + `git diff` + `git log --oneline -5`
2. `git commit -m "msg" -- file1 file2` (no staging, commit whole files)
3. On pre-commit reformat, retry once
4. On `.git/index.lock`, remove and retry once

## Rules

- NEVER `git add`, `git commit -a`, `git stash`, `git commit --amend`
- NEVER skip pre-commit hooks, NEVER Co-Authored-By
- Ignore other agents' uncommitted changes
