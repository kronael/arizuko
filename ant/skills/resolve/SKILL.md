---
name: resolve
description: >
  Classify message as new task or continuation, recall context,
  match skills. Runs on every prompt via gateway nudge.
user-invocable: false
---

# Resolve

Triage every incoming message. Internal only — never emit the section
headings below or words like "Classification:", "Continuation —", "New
task —". Wrap reasoning in `<think>…</think>`.

## 1. Classify

**Continuation** — follow-up to current work (yes, ok, corrections,
references to something just discussed). Skip to 4.

**New task** — distinct request, or first message in session. If unsure,
treat as new task.

## 2. Recall (new task only)

```bash
ls -t ~/diary/*.md 2>/dev/null | head -2 | xargs cat 2>/dev/null
ls ~/facts/ 2>/dev/null | head -20
```

Read the 2 most recent diary files. Scan fact filenames; read any
relevant to the topic. If the user references an unrecognized name:

```bash
grep -ril "<term>" ~/diary/ ~/facts/ ~/users/ 2>/dev/null | head -5
```

If a fact's `verified_at` is >14 days old and the task needs accurate
data, refresh via `/find <topic>`. Delete facts that are wrong.

## 3. Dispatch (new task only)

```bash
for d in ~/.claude/skills/*/; do
  n=$(basename "$d")
  desc=$(awk '/^description:/{f=1; sub(/^description:[[:space:]]*/,""); print; next} f && /^[^ ]/{exit} f{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  [ -n "$desc" ] && echo "$n: $desc"
done
```

Match descriptions against the request. If a skill matches, read its
SKILL.md and follow its workflow.

## 4. Act

Respond to the user. Apply matched skill workflows. Do not mention this
skill.
