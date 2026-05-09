---
name: hello
description: Send a welcome message to introduce yourself and explain what you can do.
when_to_use: Use on first contact with a new user or group, or when asked to introduce yourself.
user-invocable: true
---

# Hello

Three parts, one chat message (must fit telegram/discord without scrolling):

1. Greeting (2-3 lines) — name, what you do, "send me a message"
2. Use cases — bolded L1, indented L2 example. Omit unavailable.
3. Reference — commands

## Persona

If `~/SOUL.md` exists: read it, open the greeting in-persona using
`$ARIZUKO_GROUP_NAME` as name. Only reference `$ARIZUKO_WORLD` if it
adds context (e.g. group name doesn't already contain it) — never
force "in the $WORLD" filler if it reads awkwardly. If SOUL has a
Quirks section, weave one flavor line in.

If `~/SOUL.md` is absent: use the template below, end with exactly one
line (no nagging): `I don't have a soul yet — run /soul if you want to shape my persona.`

## Template (no-soul fallback)

```
I'm $ARIZUKO_GROUP_NAME. I can research, code, build web apps, and
help with analysis and daily tasks.

Research    "research the latest on X"
Data        Send CSVs — charts + reports back
Code        Node/Python/Go/Rust — I write, run, iterate
Web         Dashboards, pages, file sharing — "show me my web"
Files       Send images, PDFs, docs, voice
Scheduling  "weekly summary every Monday"

/new   /stop   /ping
```

The "Web" line is a tease — don't dump URLs. When the user asks, run
`/howto` (generate or confirm page) and reply with the resolved URL.

## URL verification

Before mentioning any web URL in the greeting (or at all), verify the
page exists. Never advertise a 404:

```bash
URL="https://$WEB_HOST/pub/howto/"
curl -fsI "$URL" >/dev/null 2>&1 && echo OK || echo MISSING
```

Only include the URL if the check returns `OK`. If `MISSING` (or
`$WEB_HOST` empty), omit the line entirely — do not mention web at all,
and do not link to a page that does not exist. The "Web" tease in the
template is fine; it asks the user to request, then `/howto` builds and
verifies before replying with the URL.
