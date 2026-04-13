---
name: refine
description: >
  Use after shipping a feature to simplify and clean up. Removes
  dead code, collapses verbosity, runs tests, commits [refined].
user-invocable: true
---

# Refine

Orchestrates code refinement in main context (full conversation visibility).

## Simplify First

Primary objective: make code simpler while preserving all functionality.

- ALWAYS remove dead code, redundant checks, unnecessary abstractions
- ALWAYS collapse multi-line logic that reads as clearly on one line
- ALWAYS prefer plain functions over classes when no state is held
- ALWAYS delete helpers used only once — inline them
- NEVER add comments that restate code; only non-obvious intent
- Fewer moving parts = fewer bugs, smaller surface = easier to test

## Workflow

1. **Checkpoint** - if uncommitted changes, invoke `Skill(commit, "[checkpoint]")`
2. **Validate** - run build/test, fix failures
3. **Improve** - spawn improve agent via `Task(prompt, agent="improve")`
   - Lead with: "Simplify this code: remove redundancy, collapse verbosity,
     delete dead paths. Keep all tests passing."
4. **Document** - spawn readme agent via `Task(prompt, agent="readme")`
5. **Verify** - final build/test
6. **Commit** - if changes, invoke `Skill(commit, "[refined]")`
7. **Summary** - what changed, main impact, no fluff, not marketing

## Prompt Structure

```
Intent: [user's original request, not summary]
Primary: [files to modify]
Context: [read-only reference, if needed]
```

For readme agent: list what changed (file + one-line each).

## Rules

- NEVER do improvement work yourself - delegate to improve agent
- NEVER summarize user intent - pass original request
- Explicit scope > vague "review these files"
- Run ALL 7 steps; skip commit only if no file changes
