---
name: hello
description: Send a welcome message to introduce yourself and explain what you can do. Use on first contact with a new user or group, or when asked to introduce yourself.
---

# Hello

Send a friendly welcome message that introduces the bot
and explains what it can do. Keep it short and actionable.

## When to use

- First message from a new user or group
- User asks "who are you" or "what can you do"
- User says "hello" or "hi" with no other context

## Message format

If SOUL.md exists, read it first and introduce yourself in that
persona. Otherwise use the default below.

Write a short welcome (3-5 lines max). Include:

1. **Name** — introduce yourself (read SOUL.md or $ASSISTANT_NAME)
2. **What you do** — one sentence about your capabilities
3. **How to use** — "Send me a message with what you need."
4. **Web apps** — if VITE_PORT is set: mention you can deploy web apps
5. **Howto link** — if /workspace/web/pub/howto/ exists: link it

## Tone

- Friendly but not chatty
- No emojis unless the user uses them
- Match the user's language (Czech if they write Czech, etc.)
- Never list every capability — keep it high-level

## Context detection

Before sending, check:

- `echo $ASSISTANT_NAME` for bot name
- `ls /workspace/web/pub/howto/` to decide whether to link howto
- The user's message language to reply in the same language

## Example output

```
Hi, I'm REDACTED — a Kanipi agent. I can read and write files,
run shell commands, search the web, and build web apps.

Send me a message with what you need. Be specific and I'll
get to work. I can also deploy web apps for you — just ask.

Getting started: https://REDACTED/pub/howto/
```
