---
status: spec
---

# Chat Web App

Full React/Tailwind/Vite SPA at `/pub/chat/` — the primary human-facing
interface to arizuko. Discord-style three-panel layout; Claude-style message
aesthetics. Replaces the HTMX scaffolding in `webd/pages.go`. Slink
remains a pure API surface; this app is the web channel.

---

## Problem

The current HTMX chat pages (`/chat/<folder>`) are two separate pages:
a groups grid and a per-group chat. Switching groups navigates away. There
is no thread list panel, no real sidebar, no multi-group live view, and no
path to group creation. The UX cannot scale to a user with 10+ groups and
dozens of threads.

---

## Vision

A Discord-like three-panel shell: narrow left rail listing groups, a
thread list for the selected group, and the chat pane. Every group is
one AI agent; threads are topics within that agent's conversation space.
The web is just another channel — messages sent from Telegram, Discord,
or WhatsApp appear here too, with a small platform badge.

---

## Technology

| Layer        | Choice                   | Reason                                                      |
| ------------ | ------------------------ | ----------------------------------------------------------- |
| Framework    | React 19                 | Concurrent features, stable ecosystem, Claude.ai uses it    |
| Build        | Vite 6                   | Already in the stack (vited); sub-second HMR                |
| Styling      | Tailwind CSS 4           | Utility-first, dark/light trivial, no CSS files to maintain |
| State        | Zustand                  | Minimal boilerplate; one store for selected group/topic     |
| Data         | TanStack Query v5        | Caching, background refetch, optimistic updates             |
| Real-time    | EventSource (native SSE) | Already deployed; no WebSocket infra needed                 |
| Icons        | Lucide React             | Lightweight, consistent                                     |
| Source root  | `chatapp/`               | New directory in repo; separate from webd Go code           |
| Build output | `template/web/pub/chat/` | Served by vited like all other pub pages                    |

`make chat` builds the SPA. CI bundles it; the built output is committed
to the repo (small, deterministic) so instances get it without a Node
runtime at deploy time.

---

## Layout

### Desktop — three-panel

```
┌────────────────────────────────────────────────────────────────────┐
│ ■ arizuko                                                [●] you ↓ │  ← topbar (40px)
├────────────┬───────────────────┬───────────────────────────────────┤
│            │                   │                                   │
│  GROUPS    │  THREADS          │  CHAT PANE                        │
│  (240px)   │  (280px)          │  (flex-grow)                      │
│            │                   │                                   │
│  ● atlas   │  atlas            │  atlas · #strategy                │
│  ● rhias ← │    #strategy ●    │  ──────────────────────────────── │
│  ● nemo    │    #brainstorm    │                                   │
│  ● sloth   │    #q3-planning   │  [A] atlas  12:04                 │
│  ● krons   │  ── yesterday ──  │      Good morning. What would     │
│            │    #weekly-sync   │      you like to work on today?   │
│  ──────    │    #research      │                                   │
│            │                   │  [U] you  12:06        web ⬡     │
│  + new     │  + new thread     │      Let's plan the Q3 roadmap.   │
│            │                   │                                   │
│            │                   │  [A] atlas  12:06                 │
│            │                   │      ╔═ read_file ══════════╗     │
│            │                   │      ║ ~/q3-notes.md  ↓     ║     │
│            │                   │      ╚══════════════════════╝     │
│            │                   │      Here's what I found in       │
│            │                   │      your notes…                  │
│            │                   │                                   │
│            │                   │  [A] atlas  12:07          ● ● ● │
│            │                   │                                   │
│            │                   │  ──────────────────────────────── │
│            │                   │  ┌────────────────────────────┐   │
│            │                   │  │ Message atlas…           ⏎ │   │
│            │                   │  └────────────────────────────┘   │
└────────────┴───────────────────┴───────────────────────────────────┘
```

### Mobile — single pane with nav drawer

```
┌──────────────────────┐
│ ☰  atlas · #strategy │   ← header, group + thread name
├──────────────────────┤
│                       │
│  [A] atlas  12:04     │
│      Good morning.    │
│                       │
│  [U] you  12:06       │
│      Let's plan Q3.   │
│                       │
│  [A] atlas  12:07     │
│      ● ● ●            │
│                       │
│  ─────────────────    │
│  │ Message…      ⏎ │  │
│  ─────────────────    │
├──────────────────────┤
│  ●Groups  ●Threads  ✎ │   ← bottom nav
└──────────────────────┘

Tap ☰ → slides in groups panel from left (full-screen overlay, 85% width).
Tap a group → thread list replaces panel.
Tap a thread → panel closes, chat pane shows.
Bottom nav: Groups, Threads (for current group), New Thread.
```

---

## Screens

### 1. Unauthenticated

SPA boots, calls `GET /api/me`, gets 401. Shows full-screen:

```
┌────────────────────────────────────────┐
│                                        │
│           ■ arizuko                    │
│                                        │
│      Your AI agent workspace.          │
│                                        │
│      [ Sign in with Google ]           │
│      [ Sign in with GitHub ]           │
│      or  email / password              │
│                                        │
└────────────────────────────────────────┘
```

Redirects to `/auth/login?return_to=/pub/chat/`.

---

### 2. No groups (empty state)

User authenticated but no accessible groups:

```
┌────────────┬────────────────────────────────────────┐
│            │                                        │
│  Groups    │                                        │
│            │       You don't have any agents yet.   │
│  (empty)   │                                        │
│            │       Ask your workspace admin to      │
│  ──────    │       invite you, or create one:       │
│            │                                        │
│  + new     │       [ + Create new group ]           │
│            │                                        │
└────────────┴────────────────────────────────────────┘
```

---

### 3. Group rail (left panel)

```
┌───────────────────────┐
│  ■ arizuko            │
├───────────────────────┤
│  GROUPS               │
│                       │
│  ● atlas              │  ← selected (highlighted bg)
│      Q3 roadmap · 12m │
│      ● unread         │
│                       │
│  ● rhias              │
│      sent weekly re…  │
│                       │
│  ● nemo               │
│      sure, let me…    │
│                       │
│  ● krons              │
│      (no messages)    │
│                       │
│  ────────────────     │
│  + Create group       │
│                       │
│  ────────────────     │
│  ● you                │  ← user, bottom of panel
│    /auth/logout       │
└───────────────────────┘
```

Each group row:

- Avatar: coloured circle with first letter (colour derived from folder hash)
- Group name (bold)
- Last message preview (truncated, 35 chars)
- Relative timestamp
- Unread dot (blue) when cursor hasn't seen latest message

---

### 4. Thread list (second panel)

```
┌───────────────────────────┐
│  atlas              ⚙     │
│  Personal assistant       │  ← group description from SOUL.md
├───────────────────────────┤
│  THREADS                  │
│                           │
│  ● #strategy         12m  │  ← selected, unread dot
│    Let's plan the Q3 ro…  │
│                           │
│    #brainstorm       2h   │
│    Can you explore dif…   │
│                           │
│    #weekly-sync      Mon  │
│    Here's your weekly …   │
│                           │
│    #q3-planning       Sat │
│    Here's what I found…   │
│                           │
│  ── older ──              │
│    #research         Apr  │
│                           │
│  + New thread             │
└───────────────────────────┘
```

Thread naming: first 40 chars of first user message in the thread, stored
as `topic_label` on creation. Falls back to `t<unix_ms>` if no label yet.

`⚙` → group settings modal (name, SOUL.md preview, platform links).

---

### 5. Chat pane

```
┌──────────────────────────────────────────────────────┐
│  [A] atlas  ●  ·  #strategy                    ⋯    │  ← header
├──────────────────────────────────────────────────────┤
│                                                      │
│  [A] atlas                               12:04       │
│      Good morning! What would you like              │
│      to work on today?                              │
│                                                      │
│                          you              12:06 web ⬡│
│              Let's plan the Q3 roadmap.              │
│                                                      │
│  [A] atlas                               12:06       │
│      ┌──────────────────────────────────┐           │
│      │ ⚙ read_file                 ↕   │           │
│      │   path: ~/workspace/q3.md        │           │
│      │   → 2 340 chars returned         │           │
│      └──────────────────────────────────┘           │
│      Here's what I found in your Q3 notes.          │
│      The biggest themes are…                        │
│                                                      │
│                          you              12:12 web ⬡│
│              What about the mobile product?          │
│                                                      │
│  [A] atlas  ● ● ●                        12:12       │  ← typing
│                                                      │
├──────────────────────────────────────────────────────┤
│  ┌────────────────────────────────────────────────┐  │
│  │ Message atlas…                                  │  │
│  │                                              ⏎ │  │
│  └────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

**Message anatomy:**

User messages:

- Right-aligned (no bubble, clean left edge on content)
- Platform badge top-right: `web ⬡`, `tg ✈`, `dc 🎮`, `wa ●` — shows which channel the message entered on
- Dimmer colour than agent

Agent messages:

- Left-aligned with circular avatar (group initial, same colour as rail)
- Agent name + timestamp on same line
- Tool-use cards (see §6)
- Typing indicator replaces message until first token arrives

**Timestamps**: relative by default (`12m`, `2h`, `Mon`); hover → absolute.

**Scroll**: virtual list (TanStack Virtual) for performance with 1000+ messages.
`↓ N new messages` pill appears when user scrolled up and new messages arrive.

---

### 6. Tool-use card

When the agent calls an MCP tool, a card appears inline in the message:

```
┌──────────────────────────────────────┐
│ ⚙  schedule_task          ↕ expand  │   ← collapsed by default
└──────────────────────────────────────┘

Expanded:
┌──────────────────────────────────────┐
│ ⚙  schedule_task          ↑ collapse│
│                                      │
│  name:   "Weekly report"            │
│  cron:   "0 9 * * 1"               │
│  folder: "atlas"                    │
│                                      │
│  → task_id: t_7f3a2b                │
└──────────────────────────────────────┘
```

Tool cards collapse by default to keep conversation scannable.
Error results show with a red left border.

---

### 7. New thread flow

Clicking `+ New thread` or `⌘K → new`:

```
┌──────────────────────────────────────────────┐
│                                              │
│   Start a new conversation with atlas        │
│                                              │
│   ┌──────────────────────────────────────┐  │
│   │ What would you like to discuss?      │  │
│   └──────────────────────────────────────┘  │
│                                              │
│   [ Cancel ]              [ Start thread ]  │
│                                              │
└──────────────────────────────────────────────┘
```

Pressing `Start thread` generates a topic ID (`t<unix_ms>`), navigates
to the new thread, and sends the first message. The thread label is set
from the first message server-side after creation.

No modal needed for just a blank thread — pressing `+ New thread` with
an empty prompt is fine too (just switches to an empty pane).

---

### 8. New group modal

```
┌──────────────────────────────────────────────────┐
│  Create a new group                         ✕    │
├──────────────────────────────────────────────────┤
│                                                  │
│  Name                                            │
│  ┌──────────────────────────────────────────┐   │
│  │ my-team                                  │   │
│  └──────────────────────────────────────────┘   │
│                                                  │
│  Description (shown in thread panel)             │
│  ┌──────────────────────────────────────────┐   │
│  │ Team research assistant                  │   │
│  └──────────────────────────────────────────┘   │
│                                                  │
│  Channels  ☑ Telegram  ☐ Discord  ☑ Web         │
│                                                  │
│  ── Advanced ──                                  │
│    Parent folder  [ none ▼ ]                     │
│                                                  │
│  [ Cancel ]                    [ Create group ] │
│                                                  │
└──────────────────────────────────────────────────┘
```

Posts to `POST /api/groups` → onbod `SetupGroup`. On success, the new
group appears in the rail and is selected. A first system message is
injected: "Group created. Send a message to begin."

Visible only to users whose grants include group-creation permissions
(tier 0–1 or explicit rule). The `+ Create group` button is hidden
otherwise.

---

### 9. Group settings panel

Accessible via `⚙` in the thread list header:

```
┌──────────────────────────────────────────────────┐
│  atlas · settings                           ✕    │
├──────────────────────────────────────────────────┤
│  [A]  atlas                                      │
│       Personal assistant                         │
│       Folder: atlas                              │
│                                                  │
│  Channels active                                 │
│    ● web  ● telegram  ○ discord                  │
│                                                  │
│  SOUL.md preview                                 │
│  ┌──────────────────────────────────────────┐   │
│  │ You are Atlas, a personal productivity   │   │
│  │ assistant…  (truncated, read-only)       │   │
│  └──────────────────────────────────────────┘   │
│                                                  │
│  Slink URL  https://krons.example.com/slink/…   │
│             [ copy ]                             │
│                                                  │
│                              [ Close ]          │
└──────────────────────────────────────────────────┘
```

---

### 10. Agent status indicators

Used in group rail, thread list, and chat header:

| Symbol    | Meaning                                     |
| --------- | ------------------------------------------- |
| ● (green) | Agent container running (active response)   |
| ◐ (blue)  | Thinking — container spawned, no output yet |
| ○ (dim)   | Idle (no container, awaiting message)       |
| ✕ (red)   | Circuit breaker open (3+ failures)          |

Status is polled via `GET /api/groups/<folder>/status` at 5s interval when
the group is selected, derived from gateway's container state.

---

## Component hierarchy

```
App
├── AuthGate (redirect to /auth/login if /api/me → 401)
├── Shell
│   ├── Topbar (logo, user menu)
│   ├── GroupRail
│   │   ├── GroupItem[] (avatar, name, preview, unread)
│   │   └── CreateGroupButton
│   ├── ThreadPanel
│   │   ├── GroupHeader (name, description, ⚙ button)
│   │   ├── ThreadItem[] (name, preview, time, unread)
│   │   └── NewThreadButton
│   └── ChatPane
│       ├── ChatHeader (group + thread name, agent status)
│       ├── MessageList (virtual scroll)
│       │   ├── MessageBubble (user | agent)
│       │   │   ├── Avatar
│       │   │   ├── MessageContent (markdown rendered)
│       │   │   ├── ToolCard[] (collapsible)
│       │   │   └── PlatformBadge (web/tg/dc/wa)
│       │   └── TypingIndicator
│       ├── NewMessagesPill (scroll-to-bottom CTA)
│       └── MessageInput
│           ├── Textarea (auto-grow, Shift+Enter newline)
│           └── SendButton (Enter sends, disabled while agent typing)
├── NewGroupModal (portal)
├── NewThreadModal (portal)
└── GroupSettingsPanel (portal)
```

---

## State (Zustand store)

```typescript
interface Store {
  // navigation
  selectedFolder: string | null;
  selectedTopic: string | null;
  setGroup: (folder: string) => void;
  setTopic: (topic: string) => void;

  // real-time
  agentStatus: Record<string, 'idle' | 'thinking' | 'active' | 'error'>;
  typingTopics: Set<string>; // "folder/topic" keys
  setAgentStatus: (folder: string, status: AgentStatus) => void;

  // ui
  mobilePanel: 'groups' | 'threads' | 'chat';
  setMobilePanel: (p: MobilePanel) => void;
}
```

Server data (groups, threads, messages) lives in TanStack Query cache, not
Zustand. Zustand is navigation + ephemeral UI state only.

---

## API surface

### Existing (kept, may need grant-filtering)

| Method | Path                            | Notes                                                         |
| ------ | ------------------------------- | ------------------------------------------------------------- |
| `GET`  | `/api/groups`                   | Filter to `X-User-Groups` grant scope — currently returns all |
| `GET`  | `/api/groups/<folder>/topics`   | Unchanged                                                     |
| `GET`  | `/api/groups/<folder>/messages` | Unchanged                                                     |

### New endpoints (webd)

| Method | Path                            | Body / Query                      | Response                                               |
| ------ | ------------------------------- | --------------------------------- | ------------------------------------------------------ | -------- | ------ | ------- |
| `GET`  | `/api/me`                       | —                                 | `{sub, name, groups:[]}`                               |
| `GET`  | `/api/groups/<folder>`          | —                                 | `{folder, name, description, status}`                  |
| `GET`  | `/api/groups/<folder>/status`   | —                                 | `{status: idle                                         | thinking | active | error}` |
| `GET`  | `/api/groups/<folder>/events`   | `?topic=<t>`                      | SSE stream (auth'd, replaces slink/stream for web app) |
| `POST` | `/api/groups/<folder>/messages` | `{content, topic, reply_to?}`     | `{ok, id}`                                             |
| `POST` | `/api/groups/<folder>/topics`   | `{label?, first_message?}`        | `{topic_id, label}`                                    |
| `POST` | `/api/groups`                   | `{name, description, channels[]}` | `{folder}`                                             |
| `GET`  | `/api/groups/<folder>/settings` | —                                 | `{name, soul_preview, slink_url, channels[]}`          |

`POST /api/groups/<folder>/messages` replaces the slink POST surface
for authenticated users. Slink POST continues to work for external
scripts (the skill).

`GET /api/groups/<folder>/events` is a new SSE endpoint gated by
`X-User-Sig` / JWT (via proxyd's `requireAuth`). The existing
`/slink/stream` is not touched.

---

## SSE protocol

The existing SSE event format from `hub.go` is reused:

```
event: message
data: {"role":"assistant","content":"Hello…","topic":"t1234","id":"msg-abc"}

event: typing
data: {"folder":"atlas","topic":"t1234","on":true}

event: tool_call
data: {"tool":"read_file","input":{"path":"~/q3.md"},"call_id":"c1"}

event: tool_result
data: {"call_id":"c1","output":"# Q3 notes…","error":null}
```

The chat pane subscribes to one `EventSource` per selected group+topic.
On topic switch, the old source is `.close()`d and a new one opened.

`tool_call` + `tool_result` events are paired by `call_id` and rendered
as a single collapsible `ToolCard` in the message stream.

---

## Real-time subscription strategy

```
selected group/topic → one EventSource → hub.go fan-out
```

Background groups (not selected): no open SSE. Unread counts are
updated by polling `GET /api/groups/<folder>/topics` every 30s when the
app is focused; every 5min when the tab is in the background.

On reconnect (EventSource error), exponential backoff 1s → 2s → 4s → 8s
(cap 30s), then retry. The `Last-Event-ID` header is set for seamless
resumption after brief network blips.

---

## URL structure

The SPA is served from `/pub/chat/` (public static). All routing is
client-side:

```
/pub/chat/                        → empty (no group selected)
/pub/chat/#atlas                  → group atlas, last thread
/pub/chat/#atlas/t1234567890      → specific thread
```

Hash-based routing avoids server-side 404s for deep links (no need
for a catch-all server route). Bookmarkable; the SPA reads the hash on
mount.

`/auth/login?return_to=/pub/chat/%23atlas` → returns to the right group
after login.

---

## Build setup

```
chatapp/
  index.html          SPA shell
  src/
    main.tsx
    App.tsx
    store.ts          Zustand store
    api.ts            fetch wrappers + TanStack Query keys
    sse.ts            EventSource lifecycle hook
    components/
      GroupRail.tsx
      ThreadPanel.tsx
      ChatPane.tsx
      MessageBubble.tsx
      ToolCard.tsx
      MessageInput.tsx
      …
  tailwind.config.ts
  vite.config.ts      outDir: ../template/web/pub/chat
```

`make chat`:

```bash
cd chatapp && npm ci && npm run build
```

The built output (`template/web/pub/chat/`) is committed to the repo.
Operators do not need Node at deploy time; `arizuko create` copies the
pre-built files like all other pub pages.

`make chat-dev`: runs `vite --mode development` with HMR, proxying
`/api/*` to a running webd.

---

## webd changes

1. Remove `handleGroupsPage` and `handleChatPage` (HTMX pages) — replaced by SPA.
2. Remove `GET /`, `GET /chat/{folder...}` routes from server.go (proxyd
   will serve `/pub/chat/` statically).
3. Add new `/api/me`, `/api/groups/<folder>/events`, `/api/groups/<folder>/messages`
   (POST), `/api/groups/<folder>/topics` (POST), `/api/groups` (POST),
   `/api/groups/<folder>/status` endpoints.
4. Apply grant filtering to `GET /api/groups`: intersect `AllGroups()` with
   `X-User-Groups` header (already available via proxyd middleware).
5. Serve `webd/static/` continues unchanged for `/static/style.css` (used
   by slink widget — keep for backward compat until slink widget fully removed).

---

## Slink widget removal

The slink HTML widget (`/slink/<token>` HTML page) is superseded. The
slink API surface (`/slink/<token>` POST, `/slink/stream` SSE) remains —
these are the external API used by the skill and external integrations.

`pages.go` is gutted (no more `handleGroupsPage`, `handleChatPage`).
`slink.go` keeps its API routes, removes the HTML widget rendering path.
`webd/static/style.css` can be removed once nothing references it.

---

## Theme

Default dark (matching Discord/most AI tools). One toggle stored in
`localStorage`. Tailwind `dark:` prefix covers all variants.

Palette:

```
background      #1a1b1e   (near-black, Discord bg-secondary)
surface         #25262b   (panel bg, Discord bg-primary)
border          #373a40   (subtle dividers)
text-primary    #c1c2c5   (messages, labels)
text-dim        #5c5f66   (timestamps, placeholders)
accent-blue     #4dabf7   (unread dots, active selection, send button)
accent-green    #51cf66   (agent online status)
agent-bubble    #2c2e33   (agent message background)
user-text       #e9ecef   (user message, slightly brighter)
```

Group avatar colours: 8 deterministic colours derived from `folder.hashCode() % 8`.

Light theme: near-white background, same accent palette.

---

## Markdown rendering

Agent messages are rendered as Markdown (React Markdown + remark-gfm):

- Code blocks: syntax highlighted (highlight.js, lazy-loaded)
- Tables: styled with Tailwind
- Links: open in new tab
- Images: lazy-loaded with `max-width: 100%`

User messages: plain text only (no markdown rendering — avoids accidental
formatting on user input).

---

## Keyboard shortcuts

| Key                | Action                                         |
| ------------------ | ---------------------------------------------- |
| `⌘K` / `Ctrl+K`    | Open command palette (search groups + threads) |
| `⌘[` / `⌘]`        | Previous / next group                          |
| `↑` in empty input | Edit last message                              |
| `Enter`            | Send                                           |
| `Shift+Enter`      | Newline in input                               |
| `Esc`              | Deselect thread / close modal                  |

---

## Accessibility

- All interactive elements keyboard-reachable
- `aria-live="polite"` on message list (screen reader announces new messages)
- Group and thread items: `role="listitem"` with descriptive `aria-label`
- High-contrast support via `prefers-contrast: more` media query
- Reduced-motion: disable scroll animations, typing indicator fades

---

## Implementation phases

### Phase 1 — Scaffold + auth + group list

- `chatapp/` project setup (Vite + React + Tailwind)
- `GET /api/me` in webd
- Grant-filtered `GET /api/groups` in webd
- Group rail renders; clicking a group navigates (no thread/chat yet)
- Auth redirect on 401
- Build pipeline (`make chat`)

### Phase 2 — Thread list + basic chat

- Thread list panel (from existing `/api/groups/<folder>/topics`)
- `GET /api/groups/<folder>/events` SSE endpoint in webd
- `POST /api/groups/<folder>/messages` in webd
- Chat pane: message history + send + SSE receive
- Virtual scroll (TanStack Virtual)
- Typing indicator

### Phase 3 — Tool cards + platform badges + status

- `tool_call` / `tool_result` SSE events → ToolCard
- Platform badge on user messages
- `GET /api/groups/<folder>/status` polling → agent status indicator
- Unread dot in group rail and thread list

### Phase 4 — Group + thread creation

- New thread flow (topic POST)
- New group modal (posts to onbod)
- Group settings panel
- `⌘K` command palette

### Phase 5 — Polish + mobile

- Mobile layout (drawer + bottom nav)
- Keyboard shortcuts
- Dark/light toggle
- `Last-Event-ID` SSE resumption
- Accessibility pass

---

## What this is NOT

- Not a replacement for Telegram, Discord, or WhatsApp for end users.
  Those adapters stay. This is the web channel — one more platform
  that feeds the same agents.
- Not a multi-agent orchestration dashboard (that is `/dash/`). The
  operator dashboard is for routing rules, grants, admission queues.
  This is the user chat surface.
- Not the slink public widget. Slink remains for anonymous/external
  access via a share token. This app is for authenticated users who
  belong to the instance.
