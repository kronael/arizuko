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

**TLDR:** Attach files directly — images, PDFs, voice, documents. The agent reads them all, and sends files back.

Attach files directly to your message — images seen natively, PDFs extracted, documents read as text. No special command: just attach and ask. Voice messages (WhatsApp, Telegram, Discord) arrive pre-transcribed — the agent sees the text alongside the audio path, so transcription cost is paid once at ingest, not per reply. See section 04b for sending voice back.

The agent sends files back through `send_file` — PDFs, spreadsheets, images, audio, archives — on every platform that supports attachments (Telegram, Discord, WhatsApp, email, web). Its writable area is `~/tmp/`. You can also pull files with `/file get ~/path`. Format support varies per adapter — see section 04c.

```
here's the report [report.pdf] — pull out the key metrics
/file put data.csv
send me the output as a file
# agent replies with the file attached inline

# voice messages arrive as text:
You  [voice: can you summarise the last three tickets?]
```

---

## 04b — Voice Messages

**TLDR:** The agent can reply in spoken audio on platforms that support push-to-talk. One MCP call, no captions, 5000-char cap.

Ask for a voice reply when the last inbound message was voice, or when the persona is voice-first ("speak the digest aloud"). The agent calls `send_voice` directly — text is synthesised server-side and delivered as a native push-to-talk message on Telegram and WhatsApp, an audio attachment on Discord. Adapters without a voice primitive return Unsupported; the agent falls back to `send` with the same text.

Voice resolution is a three-step lookup: explicit argument first, then `voice:` in `~/SOUL.md` frontmatter, then the `TTS_VOICE` env var. Override per call by saying "use voice X". Captions are not attached to voice — the message stands alone, so put context in a follow-up text message if needed.

```
you    summarise yesterday's incidents and read it back as voice
agent  <sends 30-second audio reply>

# explicit voice override
you    /voice voice=alloy give me a one-liner
```

---

## 04c — File Format Support Across Adapters

**TLDR:** `send_file` dispatches by extension on Telegram, by ContentType on Discord, embeds images on Bluesky, and silently no-ops on adapters without media wiring.

Each adapter routes attachments differently:

| Adapter   | Photo                | Video             | Audio                  | Other     |
| --------- | -------------------- | ----------------- | ---------------------- | --------- |
| Telegram  | `.png/.jpg/.webp`    | `.mp4/.mov/.webm` | `.mp3/.m4a/.flac/.ogg` | document  |
| Discord   | rich preview by ContentType (any extension) |
| Bluesky   | image extensions only · non-images: link in post text |
| WhatsApp  | platform-native dispatch (Baileys)          |
| X         | photo/video media types                     |
| Mastodon, Reddit, LinkedIn, Email | no adapter wiring — file silently dropped |

`.gif` becomes an animation on Telegram, a regular preview on Discord. A `.pdf` lands as a document on Telegram, a rich preview on Discord, and is unsupported on Bluesky. If a send "looks ignored," check the adapter's capability before assuming a bug.

```
# telegram
send the chart as a file → arrives as photo

# discord
send the same chart → arrives as image with preview

# bluesky
send a PDF → posts text only, link to file
```

---

## 05 — Worlds, Groups, Tiers

**TLDR:** Each user gets an isolated world. Groups nest inside it; deeper = fewer permissions.

A **world** is your top-level workspace — your own files, diary, memory, skills, and web space. Worlds are isolated from each other: no cross-reads, no shared state. Inside a world, you can nest **groups** (also called rooms): subspaces for specific projects or channels.

Every group sits on a tier. Tier 1 is your world root — full send + management tools. Tier 2 sub-groups can send but not manage routing. Tier 3+ reply in-thread and can still attach files via `send_file`. The agent knows its own tier and only exposes tools appropriate to it. Groups are the unit of ACLs, sessions, and web URLs.

```
myworld/              tier 1 — your world root
myworld/project-a/    tier 2 — a sub-group
myworld/project-a/notes/   tier 3 — nested further, reply-only
```

---

## 06 — Memory

**TLDR:** The agent remembers across sessions — diary, facts, users, compressed history. All plain markdown.

The agent maintains four knowledge layers, each a folder of markdown files under its workspace:

- **Diary** (`diary/YYYYMMDD.md`) — daily work log, written at session end. The last 14 days are injected at session start.
- **Facts** (`facts/<topic>.md`) — researched knowledge with `verified_at` timestamps, written only via the `/find` skill. Stale entries (>14 days) get refreshed before being used.
- **Users** (`users/<id>.md`) — per-user preferences, role, history. The agent adapts without you repeating yourself.
- **Episodes** — compressed session summaries, daily rolling up into weekly, then monthly. Month-old work is recalled without re-reading every message.

Tell the agent to remember something and it picks the right store. Ask "what do you know about X?" and it searches all layers at once.

```
you    remember I prefer TypeScript for all new projects
agent  Got it — noted. I'll default to TypeScript.

you    search your memory for anything about the API migration
agent  Found 3 matches across diary and facts. Migration moved
       auth to /v2/token, complete as of March 10.
```

---

## 07 — What the Agent Knows at the Start of Every Reply

**TLDR:** Autocalls inject fresh facts — current time, world, tier, session — at prompt build. No stale clocks.

Before the agent reads your message, the gateway prepends an `<autocalls>` block with facts resolved at that exact moment: current UTC time, instance name, group folder, tier, session id. The agent treats these as ground truth — no "let me check the date" round-trips.

On top of that, the last 14 days of diary and any user-specific notes are always in context. So "continue what we discussed yesterday" just works.

```
<autocalls>
  now: 2026-04-24T09:12:03Z
  instance: demo
  folder: myworld
  tier: 1
  session: s7f3a
</autocalls>
```

---

## 08 — Self-Inspection

**TLDR:** The agent can query its own routing, messages, tasks, and session state — read-only.

When you ask "why didn't that message reach the other group?" or "what tasks are scheduled?", the agent uses `inspect_routing`, `inspect_messages`, `inspect_tasks`, and `inspect_session` — read-only views into the gateway's database, scoped to its own group. No shelling out to logs or DB consoles.

For thread reconstruction the agent picks `get_thread` first — it returns the topic-scoped conversation as a clean message list, no platform-side noise. Use `fetch_history` instead when the goal is backfill or search across the whole channel; it pulls prior messages from the platform on adapters that support it (Telegram, Discord, Mastodon, Bluesky, Reddit, LinkedIn, email — everything except WhatsApp).

```
you    is my weather task still running?
agent  Yes — task is active, cron `0 9 * * *`, last run
       2026-04-24 09:00:02, next fires in 23h 48m.

you    pull the thread for #deploy and recap
agent  <get_thread → 14 messages → summary>
```

---

## 08b — Typed JIDs & Routing Patterns

**TLDR:** Routing rules use typed JIDs: `<platform>:<kind>/<id>`. Bare-id form is stale.

Every chat ID is a typed JID — `telegram:user/<id>`, `telegram:group/<id>`, `discord:<guild>/<channel>`, `discord:dm/<channel>`, `discord:user/<id>`, `mastodon:account/<id>`, `mastodon:status/<id>`, `reddit:user/<name>`, `reddit:subreddit/<name>`, `email:thread/<id>`, `email:address/<addr>`, `bluesky:user/<did>`, `bluesky:post/<uri>`, `linkedin:user/<id>`, `twitter:user/<id>`, `twitter:tweet/<id>`, `web:folder/<group>`. Old bare forms (`telegram:1234`, `reddit:t1_xyz`) still parse but no new rule should use them.

Routing patterns are globs over the typed form. `telegram:group/*` matches every Telegram group; `discord:dm/*` matches every Discord DM; `*:user/*` matches a user on any platform. Rewriting an old rule is a one-step move: `telegram:*` becomes `telegram:*/*`, then narrow the kind if you only want groups or DMs.

```
# all telegram groups → group handler
telegram:group/* → <group>

# all DMs across platforms → personal handler
*:dm/*           → <group>

# specific subreddit only
reddit:subreddit/<name> → <group>
```

---

## 09 — Web Apps

**TLDR:** Ask the agent to build an app — it deploys to your web host URL live.

The agent writes files into `/workspace/web/pub/`, served live at your group's URL. All files under `pub/` are public. Send follow-ups to iterate — the agent edits in place and changes appear immediately.

```
you   build a todo app with dark mode
agent Done. Live at: https://bot.example.com/pub/<group>/todo/

you   add a due-date field
agent Updated — reload the page.
```

---

## 09a — OAuth Multi-Account Linking

**TLDR:** Log in once, link more providers later. One canonical identity, many login methods.

Your first OAuth login becomes the canonical account. Linking a second provider (GitHub, Google, Discord, …) attaches it to the same identity — both providers now log in to the same agent, same files, same memory. Manage links from `/dash/profile`: click "Link account", complete the OAuth round-trip, done.

If the new provider's identity already belongs to another canonical user, the link page asks how to resolve it: link to the current account, or log out and become the other user. Silent duplicates never happen — every collision is a deliberate choice.

```
you    (logged in via GitHub)
       open /dash/profile → "Link Google account"
       → OAuth → returns to /dash/profile

you    (next session) log in with Google instead
       → same agent, same workspace
```

---

## 10 — Scheduling

**TLDR:** Ask the agent to schedule recurring tasks — it sets up cron jobs that run automatically.

Task types: `cron` (schedule expression), `interval` (every N ms), `once` (specific time). View or cancel: "what tasks are running?" or visit `/dash/tasks/`.

Scheduled tasks run in fresh, isolated containers — no memory injection, no diary context, no carry-over from chat. Write the prompt as a self-contained conversation: include every fact the task needs, don't reference "yesterday's discussion" or "the file we opened earlier." Voice delivery works inside tasks too — a morning digest can speak its summary by calling `/voice` or `send_voice` directly.

```
you   check the weather in London every morning at 9am
agent Scheduled at 0 9 * * *. I'll message you daily.

you   morning digest at 8am, send it as voice
agent Scheduled. Each run synthesises a fresh summary
      and replies via send_voice.

you   cancel the weather task
agent Cancelled.
```

---

## 11 — Skills

**TLDR:** Skills are slash commands that extend what the agent can do — and it can create new ones.

Type `/skill-name` at the start of your message to invoke one. The agent can also create new skills by writing a `SKILL.md` file — capabilities persist across sessions. Ask "what skills do you have?" to see the full list.

```
/hello  /self  /find  /recall-memories
/compact-memories  /diary  /web  /howto
/users  /migrate  /info  /commit

you   what skills do you have?
agent Running /self… [lists all loaded skills]
```

---

## 12 — Research & Data

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

## 13 — Coding

**TLDR:** The agent codes in any language, runs it, and sends back results.

The container ships with Node/Bun, Python 3/uv, Go 1.24, and Rust/Cargo. The agent writes code, installs packages, runs it, and iterates until it works — then sends you the output or the file. Also available: SQLite, DuckDB, PostgreSQL client, Redis client, GraphViz, pandoc, imagemagick, ffmpeg.

```
write a Go HTTP server that returns JSON health
run this Python script on my data file
convert this video to mp3 and send it back
```

---

## 14 — Dashboard & Health

**TLDR:** `/dash/` is the operator dashboard (auth-gated). `/pub/` is public static content. Adapters report `stale` when a channel goes silent.

Log in via OAuth (GitHub, Google, Discord) or Telegram login at `/auth/login`. The dashboard lives at `/dash/` — six views served by `dashd`. `/pub/` is a separate namespace for public static files (docs, web apps), not the dashboard.

| Path             | What it shows                                              |
| ---------------- | ---------------------------------------------------------- |
| `/dash/`         | Portal — one-glance health of every subsystem              |
| `/dash/status/`  | Channels, groups, active containers, errored messages      |
| `/dash/tasks/`   | Scheduled tasks · run history · pause / cancel             |
| `/dash/activity/`| Recent messages · routing decisions                        |
| `/dash/groups/`  | Routing table · per-group config · message counts          |
| `/dash/memory/`  | Browse diary, facts, users, CLAUDE.md per group            |

Each adapter publishes a `/health` endpoint that flips to `stale` when inbound traffic stops flowing, not just when the process dies. A silent Telegram bot, a dropped Mastodon stream, a logged-out WhatsApp session — all surface as unhealthy before you wonder why nobody's replying.

Messages that crash the agent get quarantined with an `errored` flag; three consecutive errors on one JID auto-reset the session so a single poisoned prompt can't wedge a group forever.

---

## 15 — Getting Your Own Agent

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

## 16 — Web Chat

**TLDR:** Chat from a browser — no app, no account needed.

Each group has a **slink** (shared link) — a token-gated URL for web chat. Anonymous users get a pseudonym; authenticated users see their real identity. Responses stream in real time via SSE.

```
# send a message
curl -X POST https://bot.example.com/slink/<token> \
  -d "content=hello&topic=t1"

# listen for responses
curl -N https://bot.example.com/slink/stream?group=<group>&topic=t1
```

Log in via OAuth for the full web UI with topics, history, and typing indicators.

---

## 16a — Slink MCP Endpoint

**TLDR:** Same slink token, different transport. `POST /slink/<token>/mcp` exposes three MCP tools to external agents.

The web chat slink is an SSE-based interactive widget for humans. The slink MCP endpoint is a separate transport designed for other agents — paste the URL into Claude Code or another MCP-aware runtime and the group becomes a tool target. The token authenticates; no JWT, no extra config.

Three tools are available: `send_message` (push a turn into the group), `steer` (mid-task redirect), and `get_round` (read the latest reply). The endpoint is stateless and idempotent — message history lives in the web chat, not here. Use the MCP transport to embed a group inside another agent's workflow; use the SSE chat for human-in-the-loop interaction.

```
# external agent registers slink MCP as a remote tool
mcp add slink https://bot.example.com/slink/<token>/mcp

# then drives the group
slink.send_message(content="run the weekly report")
slink.get_round()  # returns latest agent reply
```

---

## 17 — Channels & Routing Awareness

**TLDR:** Every platform is an adapter. Adapters differ in what they support — voice, files, reactions, threading. Build workflows around the adapter, not against it.

Each channel runs as its own adapter — Telegram, Discord, WhatsApp, Mastodon, Bluesky, Reddit, LinkedIn, email, web. Capabilities don't line up: Telegram and WhatsApp speak push-to-talk voice, Discord ships voice as an audio attachment, others return Unsupported. File extension routing varies (see 04c). Reactions exist on Telegram, Discord, Mastodon; Reddit returns Unsupported on `like`. Threading semantics differ — Discord channels vs. Telegram topics vs. Mastodon reply chains all map to the same `topic` token but the platform behaviour underneath is not identical.

When building a workflow that spans channels, branch on the inbound JID. A morning briefing scheduled to "all DMs" should call `send_voice` on Telegram, `send_file` with audio on Discord, and a plain `send` everywhere else. The agent already knows the adapter — just ask it to dispatch accordingly.

```
you   morning briefing — voice on Telegram, text elsewhere
agent <branches per JID>
      telegram:user/<id> → send_voice
      discord:dm/<id>    → send (text)
      email:address/<x>  → send (text)
```
