---
name: dispatch
description: >
  Find skills relevant to the current task. Run at the start of any
  non-trivial request to discover which skills apply. If work has already
  been done, trigger /reconcile for applicable skills.
user-invocable: false
---

# Dispatch

Scan available skills and match them to the current task.

## Scan

```bash
for d in ~/.claude/skills/*/; do
  name=$(basename "$d")
  desc=$(awk '/^description:/{found=1; sub(/^description:[[:space:]]*/,""); print; next} found && /^[^ ]/{exit} found{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$name: $desc"
done
```

## Act

Read the output. For each skill that matches the current task:

**If no work has been done yet** → invoke `/skill-name` before proceeding.

**If work has already been done** → invoke `/reconcile` with that skill name.
The reconcile skill will review prior outputs and correct them to match the
skill's requirements.

## Core skills (always active)

`self`, `diary`, `info`, `recall-memories` — do not need dispatch.

## Skip dispatch for

- Simple questions, greetings, status checks
- Tasks where you already invoked the matching skill
