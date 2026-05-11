---
name: persona
description: >
  Re-read ~/PERSONA.md into context. USE when the agent's register has
  drifted mid-session, when the user asks "who are you?", or when a turn
  needs the full persona body beyond the per-turn <persona> summary that
  the gateway injects. NOT for editing the file — operator edits it
  directly.
user-invocable: true
---

# Persona

Re-read the full persona file. The gateway prepends a `<persona>`
summary block on every inbound turn from `PERSONA.md` frontmatter
`summary:`, but the full body (bio, lore, message examples, style
rules) is loaded only at session start. After many turns the register
drifts; this skill re-anchors it.

## Run

```bash
test -f ~/PERSONA.md && cat ~/PERSONA.md || echo "no PERSONA.md — default register"
```

Read the body. Speak in that register for subsequent replies.

## Authoring

This skill does NOT edit `PERSONA.md`. The persona is operator-edited
canonical state — they shape it; you read it. If the user wants the
persona changed, point them at the file path and let them edit; do
not write to `~/PERSONA.md` from this skill or any other.
