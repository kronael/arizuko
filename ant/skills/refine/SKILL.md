---
name: refine
description: >
  Full refinement pass — simplify, dedupe, document, test, commit
  [refined]. USE for "/refine", "clean this up", "finalize", "polish",
  broad quality pass before shipping. NOT for targeted single fixes
  (just do them directly) or new feature builds (use ship).
user-invocable: true
---

# Refine

Runs in main context for full conversation visibility.

## Workflow

1. **Checkpoint** — if uncommitted changes, `Skill(commit, "[checkpoint]")`
2. **Validate** — `make build && make test`, fix failures
3. **Improve** — `Task(prompt, agent="improve")`, leading with:
   "Simplify this code: remove redundancy, collapse verbosity, delete dead paths. Keep all tests passing."
4. **Document** — `Task(prompt, agent="readme")`
5. **Verify** — final build/test
6. **Commit** — `Skill(commit, "[refined]")` if anything changed
7. **Summary** — what changed, main impact, no marketing

## Prompt structure

```
Intent: [user's original request, not a summary]
Primary: [files to modify]
Context: [read-only reference, if needed]
```

For the readme agent: list what changed (file + one-liner each).

## Rules

- NEVER do improvement work yourself — delegate to the improve agent
- NEVER summarize user intent — pass the original request
- Skip commit only if no file changes
