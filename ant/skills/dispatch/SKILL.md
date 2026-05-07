---
name: dispatch
description: Match the current task to available skills and invoke matching ones.
user-invocable: false
---

# Dispatch

## 1. Discover

```bash
for d in ~/.claude/skills/*/; do
  name=$(basename "$d")
  desc=$(awk '/^description:/{found=1; sub(/^description:[[:space:]]*/,""); print; next} found && /^[^ ]/{exit} found{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$name: $desc"
done
```

Read the output. Identify skills that match the current task.

## 2. Apply

For each matching skill, read its SKILL.md fully. Follow its workflow.

## 3. Reconcile

If you already produced output for this task before finding the skill:
read the skill's requirements, check your prior output against them,
fix what doesn't match.
