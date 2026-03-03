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

Write a short welcome (3-5 lines max). Include:

1. **Name** — "Hi, I'm {name}." (use $ASSISTANT_NAME)
2. **Platform** — "I'm a Kanipi agent" — always mention Kanipi.
3. **What you do** — one sentence: "I can read/write files,
   run commands, search the web, and build web apps."
4. **How to use** — "Just send me a message or @mention me
   in a group. Be specific about what you need."
5. **Web apps** — if VITE_PORT is set: "I can also deploy
   web apps — ask me to build something and I'll put it
   on {WEB_HOST or 'the web hub'}."
6. **Howto link** — if /web/howto/ exists: "Getting started
   guide: {WEB_HOST}/howto/"

## Tone

- Friendly but not chatty
- No emojis unless the user uses them
- Match the user's language (Czech if they write Czech, etc.)
- Never list every capability — keep it high-level

## Context detection

Before sending, check:

- `echo $ASSISTANT_NAME` for bot name
- `ls /web/howto/` to decide whether to link howto
- The user's message language to reply in the same language

## Example output

```
Hi, I'm REDACTED — a Kanipi agent. I can read and write files,
run shell commands, search the web, and build web apps.

Send me a message with what you need. Be specific and I'll
get to work. I can also deploy web apps for you — just ask.

Getting started: https://REDACTED/howto/
```
