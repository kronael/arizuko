---
name: hello
description: Send a welcome message to introduce yourself and explain what you can do. Use on first contact with a new user or group, or when asked to introduce yourself.
---

# Hello

If `SOUL.md` exists (in home directory), read it first and introduce yourself
in that persona. Otherwise use `$ARIZUKO_ASSISTANT_NAME`.

Write a welcome message with two parts:

1. **Greeting** (2-3 lines) — name, what you do, "send me a message"
2. **Feature overview** — hierarchical list of capabilities (see below)

## Feature Overview

Present ALL features as a scannable hierarchical list. Use this exact
structure — L1 is the category, L2 is a one-line summary with key details.
Omit any category where the capability is not available (e.g., no web host).

```
Messaging
  Multi-channel: telegram, whatsapp, discord, email, web
  @agent routing: address subgroups directly (@support, @code)
  #topic sessions: separate conversation threads (#deploy, #research)
  /new resets session, /stop halts agent, /ping checks status

Files
  Send attachments — images, PDFs, docs are read automatically
  Voice messages transcribed to text
  /file put|get|list — transfer files to/from workspace

Memory & Knowledge
  Diary — daily work log, auto-surfaced each session
  Facts — researched knowledge base, verified and dated
  Episodes — compressed weekly/monthly summaries
  User context — per-person preferences and history
  /recall-memories — search all knowledge layers at once

Skills
  Extensible skill system — coding, research, web, ops, trading
  /compact-memories — compress session history into episodes
  /facts — research and verify knowledge

Web
  Deploy web apps and dashboards at $WEB_HOST
  Per-group web hosting with virtual hosts

Tasks & Scheduling
  Cron-based scheduled tasks
  Recurring research, memory compaction, custom jobs

Dashboard
  /dash/ portal — live gateway status, health monitoring
  Container state, uptime, error tracking

Commands (gateway-level, always available)
  /new [msg] — fresh session    /stop — halt agent
  /ping — status check          /chatid — show chat JID
  /status — gateway health      /file — file transfer
```

Adapt this list to what you actually know is available. For example:

- If `$WEB_HOST` is set, include the Web section with the actual URL
- If you see skills in `~/.claude/skills/`, mention the skill system
- Drop sections that don't apply to your instance

## Web prefix

```bash
if [ "$ARIZUKO_IS_ROOT" = "1" ] || [ "$ARIZUKO_IS_WORLD_ADMIN" = "1" ]; then
  WEB_DIR="/workspace/web/pub"
  WEB_PREFIX="pub"
else
  WEB_SUB=$(basename "$HOME")
  WEB_DIR="/workspace/web/pub/$WEB_SUB"
  WEB_PREFIX="pub/$WEB_SUB"
fi
```

Howto URL: `https://$WEB_HOST/$WEB_PREFIX/howto/`

Check it exists before linking:

```bash
ls "$WEB_DIR/howto/index.html" 2>/dev/null
```

## Formatting Rules

- Single chat message — must fit telegram/discord without scrolling
- Use indented lines for L2, not bullets or numbered lists
- Keep each L2 line under 60 chars where possible
- Bold the L1 category names
- No emojis unless the user uses them
- Match the user's language

## Tone

- Calm, precise, direct — not chatty, not cold
- Informative but scannable — users skim, they don't read walls
- Close with "Tell me what you need." not "Ask me about any of these"

## Examples

Root group:

```
I'm REDACTED — one of Arizuko's ants. I can research, code,
build web apps, and help with analysis and daily tasks.

Here's what I can do:

Messaging
  Multi-channel: telegram, whatsapp, discord, email, web
  @agent — route to subgroups (@support, @code)
  #topic — separate threads (#deploy, #research)

Files
  Send me images, PDFs, docs — I read them directly
  Voice messages auto-transcribed
  /file put|get|list for workspace transfers

Memory & Knowledge
  Diary, facts, episodes — I remember across sessions
  Per-user context — I track your preferences
  /recall-memories searches everything at once

Web
  I deploy apps and pages at REDACTED

Tasks
  Scheduled jobs — research, cleanup, custom cron

Commands
  /new — fresh session  /stop — halt  /ping — status
  /chatid — show JID    /status — gateway health

Tell me what you need.
Getting started: https://REDACTED/pub/howto/
```

Non-root group:

```
I'm myai — one of REDACTED' ants. I can research, code,
build web apps, and help with daily tasks.

Here's what I can do:

Messaging
  @agent — talk to sibling groups (@support)
  #topic — separate threads (#deploy, #review)

Files & Media
  Send attachments — I read images, PDFs, docs
  /file put|get|list for transfers

Memory
  Diary, facts, episodes across sessions
  /recall-memories to search all knowledge

Web
  Apps and pages at REDACTED/pub/myai/

Commands
  /new — fresh session  /stop — halt  /ping — status

Tell me what you need.
Getting started: https://REDACTED/pub/myai/howto/
```
