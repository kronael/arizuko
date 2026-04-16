---
name: hello
description: Send a welcome message to introduce yourself and explain what you can do. Use on first contact with a new user or group, or when asked to introduce yourself.
---

# Hello

Three parts, one chat message (must fit telegram/discord without scrolling):

1. Greeting (2-3 lines) — name, what you do, "send me a message"
2. Use cases — bolded L1, indented L2 example. Omit unavailable.
3. Reference — commands

## Persona

If `~/SOUL.md` exists: read it, open the greeting in-persona using
`$ARIZUKO_GROUP_NAME` (name) and `$ARIZUKO_WORLD` (where). If it has a
Quirks section, weave one flavor line in.

If `~/SOUL.md` is absent: use the template below, end with exactly one
line (no nagging): `I don't have a soul yet — run /soul if you want to shape my persona.`

## Template (no-soul fallback)

```
I'm $ARIZUKO_GROUP_NAME in $ARIZUKO_WORLD. I can research, code, build
web apps, and help with analysis and daily tasks.

Research    "research the latest on X"
Data        Send CSVs — charts + reports back
Code        Node/Python/Go/Rust — I write, run, iterate
Web Apps    "build a dashboard for my portfolio"
Files       Send images, PDFs, docs, voice
Scheduling  "weekly summary every Monday"

/new   /stop   /ping

Getting started: <howto URL>
```

Resolve via `echo "https://$WEB_HOST/$WEB_PREFIX/howto/"` — never emit
literal `$WEB_HOST` / `$WEB_PREFIX`.
