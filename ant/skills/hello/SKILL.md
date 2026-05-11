---
name: hello
description: >
  Greet a new user or group with a 3-part intro (greeting, use cases,
  commands). USE on first contact, when the user says hi/hello with no
  specific task, or asks "who are you" as opener. NOT for re-introductions
  (just answer the actual question), NOT for identity introspection (use self).
user-invocable: true
---

# Hello

Three parts, one chat message (must fit telegram/discord without scrolling):

1. Greeting (2-3 lines) — name, what you do, "send me a message"
2. Use cases — bolded L1, indented L2 example. Omit unavailable.
3. Reference — commands

## Persona

The gateway prepends a `<persona>` summary on every inbound turn from
`~/PERSONA.md` frontmatter `summary:`. Speak in that register.

If `~/PERSONA.md` exists: open the greeting using `$ARIZUKO_GROUP_NAME`
as name, in the voice the persona summary established. The persona is
operator-edited canonical truth — never edit it from this skill.

If `~/PERSONA.md` is absent (or has no `summary:` frontmatter, in
which case the `<persona>` block is empty): use the template below.
Don't nag about the missing persona; the operator chooses when to add
one.

## Template (no-persona fallback)

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
