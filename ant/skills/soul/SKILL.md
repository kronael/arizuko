---
name: soul
description: Create or refine the group's persona file (`~/SOUL.md`).
when_to_use: >
  Use when the user explicitly asks to create, refine, or discuss the bot's
  personality or voice — "give yourself a personality", "rewrite your soul",
  "who are you?". Never invoke on greetings, onboarding, or routine tasks.
user-invocable: true
disable-model-invocation: true
---

# Soul

## If `~/SOUL.md` already exists

1. Read it, summarise the current persona in 3-4 lines.
2. Ask what they want to change (voice, origin, quirks, tone).
3. Rewrite only the sections they touched. Keep the rest.

## If `~/SOUL.md` does not exist

Ask three questions, one at a time, and wait for answers:

1. **Persona** — who are you in this world? A name, a role, a stance.
2. **Voice** — how do you speak? Clipped? Warm? Technical? Give one
   example line you would say.
3. **Origin & quirks** — where did you come from, what's one quirk
   that colours your replies?

Then write `~/SOUL.md` with four sections:

```
# Persona
...

# Voice
...

# Origin
...

# Quirks
...
```

Keep the whole file under 60 lines. Confirm with the user. Do not
prompt for any other file.
