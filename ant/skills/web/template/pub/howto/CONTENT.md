# Howto page — content spec

{{TAGLINE}}

Page purpose: user-facing reference for people chatting with the agent.
Tone: direct, no marketing fluff. Show commands, show what they do.

Structure: TLDR grid at top (one card per section, 2–3 words), then full sections below.
Each section: title, one-line TLDR, 2–4 paragraphs of prose, one code example.

---

## 01 — Getting Started

**TLDR:** Send a message — the agent responds. Each conversation runs in its own container.

In a direct message, every message you send reaches the agent. In a group chat, mention the bot by name or reply to one of its messages. The agent usually responds in 5–30 seconds — container startup and thinking both take a moment.

Your session continues across messages until you explicitly reset it with `/new` or it times out from inactivity. For long tasks the agent sends status messages so you know it's still working. You can send a follow-up mid-task — the agent adapts.

```
# first message
you:  summarise the three main risks in this contract

# steer mid-task
you:  actually focus only on liability clauses
```

---

## 02 — Commands

**TLDR:** Slash commands at the start of a message are intercepted before the agent sees them.

These commands are handled by the gateway — the agent never receives them. The rule is positional: `/new` at the start resets your session; "tell the agent /new context" goes straight to the agent as plain text.

| Command      | Effect                                                                                          |
| ------------ | ----------------------------------------------------------------------------------------------- |
| `/new [msg]` | Reset session. Optional message starts fresh immediately. `/new #topic` resets only that topic. |
| `/stop`      | Halt the active container immediately.                                                          |
| `/ping`      | Check the gateway is alive — responds with pong.                                                |
| `/chatid`    | Returns your current chat ID.                                                                   |
| `/file`      | `put` upload, `get` download, `ls` list workspace.                                              |

```
/new let's start over — summarise Q3 results
/file ls
# mid-message /new goes to the agent, not gateway:
can you explain /new session handling?
```

---

## 03 — Topics & Routing

**TLDR:** `#topic` for parallel threads, `@name` for child agents.

Any message containing `#word` routes to that topic's dedicated session. The agent only sees the history for that topic — your main session stays untouched. Use this to run parallel work streams without contexts bleeding into each other. Reset a single topic with `/new #topic`.

Put `@name` anywhere in your message to address a specific child agent — `@support`, `@code`, `@research`. Each child has its own personality, instructions, and conversation history. The routing token is stripped before the agent sees your message.

```
#research start digging into WASM runtimes
#deploy any issues with last night's rollout?
@support I have a billing question
# support agent receives: "I have a billing question"
/new #deploy   # reset only the deploy thread
```

---

## 04 — Files & Media

**TLDR:** Send files directly — images, PDFs, voice, documents. The agent reads them all.

Attach files directly to your message — images seen natively, PDFs extracted, documents read as text. No special command: just attach and ask. Voice messages (WhatsApp, Telegram, Discord) are automatically transcribed before the agent sees them.

To retrieve files the agent produced, use `/file get ~/path` or ask the agent to send it. The agent's writable area is `~/tmp/`.

```
here's the report [report.pdf] — pull out the key metrics
/file put data.csv
/file get ~/tmp/summary.md
send me the output as a file

# voice messages arrive as text:
You  [voice: can you summarise the last three tickets?]
```

---

## 05 — Memory

**TLDR:** The agent remembers across sessions — diary, facts, user preferences, compressed history.

The agent maintains several knowledge layers that persist between sessions:

- **Diary** — daily work log (`diary/YYYYMMDD.md`), written at session end, surfaced at next session start. Last 14 days injected automatically.
- **Facts** — researched knowledge with verification timestamps. The agent checks facts before answering and refreshes stale entries (>14 days) automatically.
- **User context** — per-user preferences, role, and history stored in `users/<id>.md`. The agent adapts without you repeating yourself.
- **Episodes** — compressed session summaries (daily → weekly → monthly). The agent remembers month-old work without re-reading everything.

Tell the agent to remember something and it picks the right store. Ask "what do you know about X?" and it searches all layers at once.

```
you    remember I prefer TypeScript for all new projects
agent  Got it — noted. I'll default to TypeScript.

you    search your memory for anything about the API migration
agent  Found 3 matches across diary and facts. Migration moved
       auth to /v2/token, complete as of March 10.
```

---

## 06 — Web Apps

**TLDR:** Ask the agent to build an app — it deploys to your web host URL live.

The agent writes files into `/workspace/web/pub/`, served live at your group's URL. All files under `pub/` are public. Send follow-ups to iterate — the agent edits in place and changes appear immediately.

```
you   build a todo app with dark mode
agent Done. Live at: https://bot.example.com/pub/mygroup/todo/

you   add a due-date field
agent Updated — reload the page.
```

---

## 07 — Scheduling

**TLDR:** Ask the agent to schedule recurring tasks — it sets up cron jobs that run automatically.

Task types: `cron` (schedule expression), `interval` (every N ms), `once` (specific time). Scheduled tasks run isolated with a self-contained prompt — write the prompt as if it's a new conversation. View or cancel: "what tasks are running?" or visit `/dash/tasks/`.

```
you   check the weather in London every morning at 9am
agent Scheduled "weather-london" at 0 9 * * *. I'll message you daily.

you   cancel the weather task
agent Cancelled.
```

---

## 08 — Skills

**TLDR:** Skills are slash commands that extend what the agent can do — and it can create new ones.

Type `/skill-name` at the start of your message to invoke one. The agent can also create new skills by writing a `SKILL.md` file — capabilities persist across sessions. Ask "what skills do you have?" to see the full list.

```
/hello  /self  /facts  /recall-memories
/compact-memories  /diary  /web  /howto
/users  /migrate  /info  /commit

you   what skills do you have?
agent Running /self… [lists all loaded skills]
```

---

## 09 — Research & Data

**TLDR:** The agent browses the web, analyzes data, and produces charts and reports.

Ask it to research any topic — it runs live searches, opens pages, and returns a summary with sources. For complex sites it uses a browser to interact with forms and take screenshots. Video and audio URLs are handled with yt-dlp; transcription via Whisper.

Send data and the agent analyzes it: CSV, Excel, or paste numbers directly. It produces charts (matplotlib, plotly), Excel files (openpyxl), PowerPoint decks (python-pptx), and PDF reports (weasyprint).

```
research the latest on quantum error correction
download and transcribe https://youtu.be/abc123
analyze this CSV and plot monthly totals
turn this data into a PowerPoint slide deck
```

---

## 10 — Coding

**TLDR:** The agent codes in any language, runs it, and sends back results.

The container ships with Node/Bun, Python 3/uv, Go 1.24, and Rust/Cargo. The agent writes code, installs packages, runs it, and iterates until it works — then sends you the output or the file. Also available: SQLite, DuckDB, PostgreSQL client, Redis client, GraphViz, pandoc, imagemagick, ffmpeg.

```
write a Go HTTP server that returns JSON health
run this Python script on my data file
convert this video to mp3 and send it back
```

---

## 11 — Dashboard

**TLDR:** The `/dash/` portal shows gateway status, tasks, groups, memory, and activity.

Log in via OAuth (Google, GitHub, Discord) or Telegram login. Pause or cancel tasks, browse per-group memory and configuration, and approve onboarding requests — all from one place.

```
https://bot.example.com/dash/

Status      channels · groups · active containers · errors
Tasks       scheduled jobs · run history · pause / cancel
Groups      routing table · group config · message counts
Memory      browse MEMORY.md · CLAUDE.md per group
Activity    last 50 messages · routing decisions
Onboarding  approve / reject new user requests
```

---

## 12 — Getting Your Own Agent

**TLDR:** Message the gateway and request a world — the operator approves and you get your own agent.

I'm {{BOT_NAME}}, living in {{WORLD}}. You can have your own too. When an instance has onboarding enabled, messaging the gateway as an unrecognized user starts a setup flow. Pick a name for your world, wait for operator approval, and you get a dedicated agent with its own workspace, memory, and web space. Your agent is isolated — separate sessions, separate files, separate personality.

```
you       (first message to the gateway)
gateway   Welcome! Pick a name for your world.
you       myworld
gateway   Request submitted. Waiting for approval.

# operator approves — you get a welcome message
agent     I'm myworld — your dedicated agent. Tell me what you need.
```

---

## 13 — Web Chat

**TLDR:** Chat from a browser — no app, no account needed.

Each group has a **slink** (shared link) — a token-gated URL for web chat. Anonymous users get a pseudonym; authenticated users see their real identity. Responses stream in real time via SSE.

```
# send a message
curl -X POST https://bot.example.com/slink/a1b2c3d4e5f67890 \
  -d "content=hello&topic=t1"

# listen for responses
curl -N https://bot.example.com/slink/stream?group=mygroup&topic=t1
```

Log in via OAuth for the full web UI with topics, history, and typing indicators.
