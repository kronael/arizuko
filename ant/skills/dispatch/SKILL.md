---
name: dispatch
description: >
  Find skills relevant to the current task and invoke them. If work has
  already been done without a matching skill, retroactively correct prior
  outputs against the skill's requirements.
user-invocable: false
---

# Dispatch

Scan available skills, match to the current task, invoke or reconcile.

## Scan

```bash
for d in ~/.claude/skills/*/; do
  name=$(basename "$d")
  desc=$(awk '/^description:/{found=1; sub(/^description:[[:space:]]*/,""); print; next} found && /^[^ ]/{exit} found{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$name: $desc"
done
```

Read the output. Identify every skill that matches the current task.

## Act

For each matching skill:

**No work done yet** → invoke `/skill-name` before proceeding.

**Work already done** → reconcile:

1. Read the skill: `cat ~/.claude/skills/<name>/SKILL.md`
2. Extract requirements, conventions, output formats, constraints
3. Review the conversation from where the task started — identify outputs
   you produced: files, messages, decisions, code
4. Diff each output against the skill's requirements
5. Correct violations: edit files, redo decisions, fix formats
6. If redoing work is expensive (long research, large generation), ask user first
7. If a skill contradicts what the user explicitly asked, preserve the user's
   instruction and note the conflict

## Core skills (always active)

`self`, `diary`, `info`, `recall-memories` — do not need dispatch.

## When a skill itself requires reconciliation

Some skills have strict conventions (output format, naming, commit style,
file paths). When you invoke ANY skill and it has requirements that conflict
with work already done in this conversation — reconcile that work immediately.
Do not wait for dispatch to tell you. Reading a skill's requirements IS the
trigger to check prior work against them.

## Skip dispatch for

- Simple questions, greetings, status checks
- Tasks where you already invoked the matching skill
