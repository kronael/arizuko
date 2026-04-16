---
name: resolve
description: >
  Classify message as new task or continuation, recall context,
  match skills. Runs on every prompt via gateway nudge.
user-invocable: false
---

# Resolve

Triage every incoming message. Continuations must exit in <3s.

**Internal only.** Never emit the section headings below (`## Classify`,
`## Recall`, `## Dispatch`, `## Act`) or the words "Classification:",
"Continuation —", "New task —" to the user. Wrap any reasoning in
`<think>…</think>`. The user sees the result, not the triage.

## 1. Classify

**Continuation** -- follow-up to current work (yes, do it, ok,
corrections, references to something just discussed). Skip to 4.

**New task** -- distinct request unrelated to previous turn, or
first message in session. If unsure, treat as new task.

## 2. Recall (new task only)

```bash
ls -t ~/diary/*.md 2>/dev/null | head -2 | xargs cat 2>/dev/null
ls ~/facts/ 2>/dev/null | head -20
```

Read the 2 most recent diary files. Scan fact filenames. If any
fact is relevant to the message topic, read it. If the user
references an unrecognized name or term:

```bash
grep -ril "<term>" ~/diary/ ~/facts/ ~/users/ 2>/dev/null \
  | head -5
```

Read matches. Goal: load context BEFORE responding.

**Fact freshness**: when you read a fact, check its `verified_at`
date. If older than 14 days and the task needs accurate data,
re-research it now with `/facts <topic>`. Delete facts that are
wrong or no longer relevant.

## 3. Dispatch (new task only)

```bash
for d in ~/.claude/skills/*/; do
  n=$(basename "$d")
  desc=$(awk '/^description:/{f=1; sub(/^description:[[:space:]]*/,""); print; next} f && /^[^ ]/{exit} f{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$n: $desc"
done
```

Match descriptions against the user's request. If a skill
matches, read its SKILL.md and follow its workflow.

## 4. Act

Respond to the user. Apply matched skill workflows.

Do not mention this skill to the user.
