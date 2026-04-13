---
name: hello
description: Send a welcome message to introduce yourself and explain what you can do. Use on first contact with a new user or group, or when asked to introduce yourself.
---

# Hello

If `SOUL.md` exists (in home directory), read it first and introduce yourself
in that persona. Otherwise use `$ARIZUKO_ASSISTANT_NAME`.

Write a welcome message with three parts:

1. **Greeting** (2-3 lines) — name, what you do, "send me a message"
2. **Use cases** — what concrete things users can ask you to do
3. **Reference** — commands and mechanics

## Use Cases

Lead with what the user can DO, not how the system works. Present as a
scannable list — L1 is the use case, L2 is a concrete example.

```
Research
  "research the latest on quantum error correction"
  "compare the top 3 competitors to X"

Data & Analysis
  "analyze this CSV and plot monthly trends"
  Send spreadsheets, CSVs — get charts and reports back

Code
  "write a Go HTTP server that returns JSON health"
  Node/Python/Go/Rust — writes, runs, iterates, sends results

Web Apps
  "build a todo app with dark mode"
  Deploys live to your URL, iterate with follow-ups

Files & Media
  Send images, PDFs, docs, voice — read automatically
  /file put|get|ls for workspace transfers

Scheduling
  "check the weather every morning at 9am"
  Cron jobs, intervals, one-shot tasks
```

Omit any category where the capability is not available.

## Reference

After the use cases, add a compact reference section:

```
Commands
  /new [msg] — fresh session    /stop — halt agent
  /ping — status check          /chatid — show chat JID
  /file — file transfer

Routing
  @agent — address child groups    #topic — parallel threads
```

Omit routing if no child groups exist.

## Web prefix

You MUST run this bash to get the howto URL — never guess it:

```bash
echo "https://$WEB_HOST/$WEB_PREFIX/howto/"
```

Use the exact URL printed. NEVER output `$WEB_HOST` or `$WEB_PREFIX` literally.

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

Research
  "research the latest on X" — live web search + summary
  "download and transcribe this video"

Data & Analysis
  Send spreadsheets, CSVs — I produce charts and reports
  "run a SQL query over this data"

Code
  Node/Python/Go/Rust — I write, run, and iterate
  "build a CLI tool that deduplicates CSV rows"

Web Apps
  "build a dashboard for my portfolio"
  Deployed live at REDACTED

Files
  Send images, PDFs, docs, voice — I read them directly
  /file put|get|ls for workspace transfers

Scheduling
  "send me a weekly summary every Monday"
  Cron jobs, intervals, one-shot tasks

Commands
  /new — fresh session  /stop — halt  /ping — status
  @agent — child groups  #topic — parallel threads

Tell me what you need.
Getting started: https://REDACTED/pub/howto/
```

Non-root group:

```
I'm myai — one of REDACTED's ants. I can research, code,
build web apps, and help with daily tasks.

Here's what I can do:

Research
  "research X" — web search, summaries, sources
  "open this page and screenshot the pricing table"

Data
  Send CSVs, spreadsheets — charts and reports back
  "analyze this and plot monthly trends"

Code
  Node/Python/Go/Rust — writes, runs, sends results
  "convert this video to mp3"

Web Apps
  "build a todo app with dark mode"
  Live at REDACTED/pub/myai/

Files
  Send images, PDFs, docs, voice — read automatically
  /file put|get|ls for transfers

Commands
  /new — fresh session  /stop — halt  /ping — status

Tell me what you need.
Getting started: https://REDACTED/pub/myai/howto/
```
