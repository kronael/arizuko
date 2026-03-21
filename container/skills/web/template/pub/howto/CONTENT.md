# Howto page — content spec

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

## 02 — Gateway Commands

**TLDR:** Slash commands at the start of a message are intercepted before the agent sees them.

These commands are handled by the gateway — the agent never receives them. The rule is positional: `/new` at the start resets your session; "tell the agent /new context" goes straight to the agent as plain text.

| Command      | Effect                                                                                          |
| ------------ | ----------------------------------------------------------------------------------------------- |
| `/new [msg]` | Reset session. Optional message starts fresh immediately. `/new #topic` resets only that topic. |
| `/stop`      | Halt the active container immediately.                                                          |
| `/ping`      | Check the gateway is alive — responds with pong.                                                |
| `/chatid`    | Returns your current chat ID.                                                                   |
| `/status`    | Show whether a container is currently running.                                                  |
| `/file`      | `put` upload, `get` download, `ls` list workspace.                                              |

```
/new let's start over — summarise Q3 results
/file ls
# mid-message /new goes to the agent, not gateway:
can you explain /new session handling?
```

---

## 03 — Topic Sessions

**TLDR:** `#topic` in your message routes to a named session — separate context, same agent.

Any message containing `#word` anywhere routes to that topic's dedicated session. The agent only sees the history for that topic — your main session stays untouched. Use this to run parallel work streams without contexts bleeding into each other.

Reset a single topic with `/new #topic` — clears only that thread.

```
#research start digging into WASM runtimes
#deploy any issues with last night's rollout?
#research what have you found so far?
/new #deploy   # reset only the deploy thread
```

---

## 04 — Agent Routing

**TLDR:** `@name` in your message routes to a child agent named `name`.

Put `@name` anywhere in your message to address a specific child agent — `@support`, `@code`, `@research`. Each child agent has its own personality, instructions, and conversation history. The routing token is stripped before the agent sees your message. If the named child doesn't exist, the message falls back to your default agent.

```
@support I have a billing question
# support agent receives: "I have a billing question"

hey @code can you review this snippet?
```

---

## 05 — Files & Attachments

**TLDR:** Send files directly — the agent reads them. Get files back via `/file` or as attachments.

Attach files directly to your message — images seen natively, PDFs extracted, documents read as text. No special command: just attach and ask. To retrieve files the agent produced, use `/file get ~/path` or ask the agent to send it. The agent's writable area is `~/tmp/`.

```
here's the report [report.pdf] — pull out the key metrics
/file put data.csv
/file ls
/file get ~/tmp/summary.md
send me the output as a file
```

---

## 06 — Voice Messages

**TLDR:** Voice messages are automatically transcribed — the agent gets the text.

WhatsApp voice notes, Telegram voice messages, and Discord audio are all supported. Transcription happens before the agent processes the message — no action needed. The agent never receives the audio file, only the transcription text. Your operator can configure additional language passes for multilingual accuracy.

```
# what the agent sees
You  [voice/auto→en: can you summarise the last three tickets?]

# multi-language (operator configured Czech)
You  [voice/auto→cs: potřebuji pomoc s nasazením]
```

---

## 07 — Memory & Diary

**TLDR:** The agent keeps a daily diary and persistent memory — it remembers across sessions.

The diary (`diary/YYYYMMDD.md`) is written at the end of each session and surfaced at the start of the next. `MEMORY.md` holds stable facts — preferences, ongoing projects, decisions — and is always loaded. To influence memory, tell the agent something important and ask it to remember. Clearing a session with `/new` resets the conversation but does not wipe memory.

```
you    remember I prefer TypeScript for all new projects
agent  Got it — noted in MEMORY.md. I'll default to TypeScript.
```

---

## 08 — User Context

**TLDR:** The agent tracks per-user context — your preferences, role, and history.

Each user gets a file at `users/<your-id>.md` in the agent's workspace. The agent reads and updates it each session, storing your role, expertise level, ongoing work, and stated preferences — without you having to repeat yourself.

```
## Role
Backend developer — Go preferred.

## Active work
- Auth service migration (started 2025-06-10)

## Preferences
- Concise answers, no preamble
- Show diffs, not full file rewrites
```

---

## 09 — Facts System

**TLDR:** `/facts` creates verified knowledge entries the agent checks before answering.

When you ask something factual, the agent runs `/recall-memories` first. If no fresh match exists, it uses `/facts` to research and write a new entry. Facts live in `facts/*.md` with YAML frontmatter and go stale after 14 days — the agent refreshes them automatically. You can seed facts manually: "create a fact about our deployment process."

```yaml
---
summary: Deploy uses make deploy → ansible → systemd restart
verified_at: 2025-06-11
---
Run `make deploy`. Ansible pushes image, restarts service.
```

---

## 10 — Episodes & Compression

**TLDR:** Long sessions get compressed into episode summaries — the agent remembers month-old work without re-reading everything.

The `/compact-memories` skill rolls up raw session transcripts: daily sessions → weekly episodes → monthly. The gateway injects these summaries as context at every new session start — so the agent arrives knowing what you worked on last month.

```
/compact-memories episodes day   # compress yesterday's sessions
/compact-memories episodes week  # roll up daily → weekly
/compact-memories diary week     # compress diary entries
```

---

## 11 — Recall Search

**TLDR:** `/recall-memories` searches all knowledge stores at once — diary, facts, episodes, memory, user context.

The agent runs this proactively before answering questions that touch long-term context. Matching uses `summary:` frontmatter in each file. If a match looks stale the agent refreshes it before responding. You can also ask naturally: "what do you know about project Y?"

```
you   search your memory for anything about the API migration
agent Found 3 matches: diary/20250310.md, facts/backend.md,
      episodes/2025-W10.md. Migration moved auth to /v2/token,
      complete as of March 10.
```

---

## 12 — Web Apps

**TLDR:** Ask the agent to build an app — it deploys to your web host URL live.

The agent writes files into `/workspace/web/`, served live at your group's URL. Files under `pub/` are public; `priv/` requires login. Send follow-ups to iterate — the agent edits in place and changes appear immediately.

```
you   build a todo app with dark mode
agent Done. Live at: https://bot.example.com/mygroup/todo/

you   add a due-date field
agent Updated — reload the page.
```

---

## 13 — Scheduling

**TLDR:** Ask the agent to schedule recurring tasks — it sets up cron jobs that run automatically.

Task types: `cron` (schedule expression), `interval` (every N ms), `once` (specific time). Scheduled tasks run isolated with a self-contained prompt — write the prompt as if it's a new conversation. View or cancel: "what tasks are running?" or visit `/dash/tasks/`.

```
you   check the weather in London every morning at 9am
agent Scheduled "weather-london" at 0 9 * * *. I'll message you daily.

you   cancel the weather task
agent Cancelled.
```

---

## 14 — Dashboard

**TLDR:** The `/dash/` portal shows gateway status, tasks, groups, memory, and activity — no CLI needed.

Log in with your operator password. Pause or cancel tasks, edit `MEMORY.md` and `CLAUDE.md` directly in the browser, and approve onboarding requests — all from one place.

```
https://bot.example.com/dash/

Status      container uptime · error counts · last activity
Tasks       scheduled jobs · next run · pause / cancel
Groups      routing table · group config · tiers
Memory      edit MEMORY.md · CLAUDE.md per group
Activity    last 50 messages · routing decisions
Onboarding  approve / reject new user requests
```

---

## 15 — Skills

**TLDR:** Skills are slash commands that extend what the agent can do.

Type `/skill-name` at the start of your message to invoke one. The agent can also create new skills itself by writing a `SKILL.md` — capabilities extend across sessions. Run `/migrate` to push the latest skills to all groups.

```
/hello  /self  /facts  /recall-memories
/compact-memories  /diary  /web  /howto
/users  /migrate  /info  /git-repo

you   what skills do you have?
agent Running /self… [lists all loaded skills]
```

---

## 16 — Research & Web

**TLDR:** The agent can browse the web, read pages, download videos, and extract text from any document.

Ask it to research any topic and it runs live searches, opens pages, and returns a summary. For complex sites it uses the browser to interact with forms and take screenshots. Video and audio URLs are handled with yt-dlp; transcription via Whisper. PDFs and DOCXs convert to readable text via pandoc.

```
research the latest on quantum error correction
download and transcribe https://youtu.be/abc123
summarize the PDF I just sent
```

---

## 17 — Data & Visualization

**TLDR:** Send data — the agent analyzes it and sends charts back as files.

Attach a CSV or Excel file (or paste numbers directly) and tell the agent what you want. It uses pandas, numpy, matplotlib, and plotly to produce charts, then sends the image back. Beyond charts: Excel via openpyxl, PowerPoint decks via python-pptx, PDF reports via weasyprint.

```
analyze this CSV and plot monthly totals
make a bar chart of these numbers: 12, 45, 38, 72
turn this data into a PowerPoint slide deck
```

---

## 18 — Coding & Dev

**TLDR:** The agent codes in any language, runs it, and sends back results.

The container ships with Node/Bun, Python 3/uv, Go 1.24, and Rust/Cargo. The agent writes code, installs packages, runs it, and iterates until it works — then sends you the output or the file. Linting built in: biome, ruff, prettier.

```
write a Go HTTP server that returns JSON health
run this Python script on my data file
convert this video to mp3 and send it back
```

---

## 19 — Groups & Tiers

**TLDR:** Groups are nested — worlds contain children which contain grandchildren.

Every group lives at a tier: root (system), world admin, child, or grandchild. All groups sharing the same first path segment belong to the same world and share `/workspace/share`. With template routing, a message from an unknown user automatically spawns a dedicated child group — giving each user their own isolated agent.

```
atlas/             # tier 1 — world admin
atlas/support      # tier 2 — child group
atlas/support/ops  # tier 3 — grandchild
```

---

## 20 — Onboarding

**TLDR:** New users request a world with `/request` — operators approve with `/approve`.

When an unrecognized user messages the gateway, they are prompted to run `/request myworld`. The root operator receives a notification and can approve or reject from any channel. Pending requests appear in `/dash/onboarding/` with copyable commands. On approval, a world is created from the root prototype. Requires `ONBOARDING_ENABLED=1` in the gateway config.

```
# user
/request myworld

# operator
/approve telegram:-123456789
/reject  telegram:-123456789
```
