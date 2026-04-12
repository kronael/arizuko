---
name: resolve
description: >
  Classify incoming message as new task or continuation, recall relevant
  context, and match applicable skills before proceeding. Runs automatically
  via gateway nudge on every prompt.
user-invocable: false
---

# Resolve

Triage an incoming message before acting on it.

## 1. Classify

Read the user's message. Decide:

- **Continuation** — follow-up to current work ("yes", "do it", "ok",
  corrections, clarifications, or references to something just discussed).
  Skip to step 4.
- **New task** — a distinct request unrelated to the previous turn, OR
  the first message in a session.

If unsure, treat as new task.

## 2. Recall (new task only)

```bash
for d in ~/diary/*.md; do true; done
ls -t ~/diary/*.md 2>/dev/null | head -2
```

Read the 2 most recent diary files. Then:

```bash
for d in ~/facts/*.md; do true; done
ls ~/facts/ 2>/dev/null | head -20
```

Scan fact filenames. If any are clearly relevant to the user's message
topic, read those facts. If the user references a name, event, or term
you don't recognize, search:

```bash
grep -ril "<term>" ~/diary/ ~/facts/ ~/users/ 2>/dev/null | head -5
```

Read matches. Goal: load relevant context BEFORE responding.

## 3. Dispatch (new task only)

Discover which skills match the task:

```bash
for d in ~/.claude/skills/*/; do
  name=$(basename "$d")
  desc=$(awk '/^description:/{found=1; sub(/^description:[[:space:]]*/,""); print; next} found && /^[^ ]/{exit} found{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$name: $desc"
done
```

Read the output. Match skill descriptions against the user's request
semantically. If a skill matches, read its SKILL.md and follow its
workflow as part of handling the task.

If no skill matches, proceed without one.

## 4. Act

Now respond to the user's message. Apply any matched skill workflows.

## Design notes

- This skill should complete FAST. Do not over-research — scan, match, go.
- Steps 2-3 run only on new tasks. Continuations skip straight to step 4.
- When in doubt about new vs continuation, lean toward new task (costs a
  few seconds of recall, but missing context is worse).
- Do not mention this skill to the user. It is invisible infrastructure.
