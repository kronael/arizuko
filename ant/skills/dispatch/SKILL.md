---
name: dispatch
description: Find skills relevant to the current task. Run at the start of any non-trivial request to discover which skills apply, then invoke them.
user-invocable: false
---

# Dispatch

Run this before starting any non-trivial task. It reads all available skill
descriptions and returns the ones relevant to the current task.

```bash
for d in ~/.claude/skills/*/; do
  name=$(basename "$d")
  desc=$(awk '/^description:/{found=1; sub(/^description:[[:space:]]*/,""); print; next} found && /^[^ ]/{exit} found{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$name: $desc"
done
```

Read the output. For each skill that matches the current task, invoke it with
`/skill-name` before proceeding.

Core skills always active (do not need to dispatch):
- `self`, `diary`, `info`, `recall-memories`
