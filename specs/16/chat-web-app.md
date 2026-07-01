---
status: draft
---

# Chat Web App

Full React/Tailwind/Vite SPA at `/pub/chat/` — the primary human-facing
interface to arizuko. Discord-style three-panel layout, Claude-style message
aesthetics. Replaces the HTMX scaffolding in `webd/pages.go`. Slink remains a
pure API surface; this app is the web channel.

## Problem

The current HTMX chat pages (`/chat/<folder>`) are two separate pages: a groups
grid and a per-group chat. Switching groups navigates away. There is no thread
list panel, no real sidebar, no multi-group live view, and no path to group
creation. The UX cannot scale to a user with 10+ groups and dozens of threads.

## Vision

A Discord-like three-panel shell: narrow left rail listing groups, a thread
list for the selected group, and the chat pane. Every group is one AI agent;
threads are topics within that agent's conversation space. The web is just
another channel — messages sent from Telegram, Discord, or WhatsApp appear
here too, with a small platform badge.

Mobile collapses to a single pane with a nav drawer (groups → threads → chat).

## Technology

| Layer        | Choice                   | Reason                                          |
| ------------ | ------------------------ | ----------------------------------------------- |
| Framework    | React 19                 | Concurrent features, stable ecosystem           |
| Build        | Vite 6                   | Already in the stack (vited); sub-second HMR    |
| Styling      | Tailwind CSS 4           | Utility-first, dark/light trivial               |
| State        | Zustand                  | Navigation + ephemeral UI state only            |
| Data         | TanStack Query v5        | Caching, background refetch, optimistic updates |
| Real-time    | EventSource (native SSE) | Already deployed; no WebSocket infra needed     |
| Source root  | `chatapp/`               | New directory; separate from webd Go code       |
| Build output | `template/web/pub/chat/` | Served by vited like all other pub pages        |

The built output (`template/web/pub/chat/`) is committed to the repo (small,
deterministic) so instances get it without a Node runtime at deploy time.
`make chat` builds it (`cd chatapp && npm ci && npm run build`); `make chat-dev`
runs `vite --mode development` with HMR, proxying `/api/*` to a running webd.

## Data model

- **Group** = folder = one agent: `{folder, name, description, status}`.
  `status ∈ idle | thinking | active | error`, derived from runed container
  state. `description` comes from the group's `PERSONA.md`.
- **Thread** = topic within a group: `{topic_id, label, last_message_at}`.
  `label` = first 40 chars of the first user message, stored as `topic_label`
  on creation; falls back to `t<unix_ms>` if no label yet.
- **Message** = `{id, role, content, topic, platform, ts, reply_to?}`.
  `platform ∈ web | tg | dc | wa | …` — which channel the message entered on;
  shown as a badge on user messages. Agent messages render as Markdown; user
  messages are plain text.
- **Tool call** = paired `tool_call` + `tool_result` SSE events keyed by
  `call_id`, rendered as one collapsible card in the message stream.

Navigation state (`selectedFolder`, `selectedTopic`, per-group `agentStatus`,
`typingTopics`, mobile panel) lives in Zustand. All server data (groups,
threads, messages) lives in the TanStack Query cache, not Zustand.

## API surface

### Existing (kept, may need grant-filtering)

| Method | Path                            | Notes                               |
| ------ | ------------------------------- | ----------------------------------- |
| `GET`  | `/api/groups`                   | Intersect with caller's grant scope |
| `GET`  | `/api/groups/<folder>/topics`   | Unchanged                           |
| `GET`  | `/api/groups/<folder>/messages` | Unchanged                           |

### New endpoints (webd)

| Method | Path                            | Body / Query                      | Response                                           |
| ------ | ------------------------------- | --------------------------------- | -------------------------------------------------- |
| `GET`  | `/api/me`                       | —                                 | `{sub, name, groups:[]}`                           |
| `GET`  | `/api/groups/<folder>`          | —                                 | `{folder, name, description, status}`              |
| `GET`  | `/api/groups/<folder>/status`   | —                                 | `{status}` (idle/thinking/active/error)            |
| `GET`  | `/api/groups/<folder>/events`   | `?topic=<t>`                      | SSE stream (auth'd; web-app replacement for slink) |
| `POST` | `/api/groups/<folder>/messages` | `{content, topic, reply_to?}`     | `{ok, id}`                                         |
| `POST` | `/api/groups/<folder>/topics`   | `{label?, first_message?}`        | `{topic_id, label}`                                |
| `POST` | `/api/groups`                   | `{name, description, channels[]}` | `{folder}` → onbod `SetupGroup`                    |
| `GET`  | `/api/groups/<folder>/settings` | —                                 | `{name, soul_preview, slink_url, channels[]}`      |

`POST /api/groups/<folder>/messages` replaces the slink POST surface for
authenticated users; slink POST keeps working for external scripts (the skill).
`GET /api/groups/<folder>/events` is a new SSE endpoint gated by proxyd's
`requireAuth` (JWT); the existing `/slink/stream` is untouched. `POST
/api/groups` is visible only to callers whose grants allow group creation.

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

The chat pane subscribes to one `EventSource` per selected group+topic; on
topic switch the old source is closed and a new one opened. Background groups
have no open SSE — unread counts poll `/api/groups/<folder>/topics` (30s when
focused, 5min when backgrounded). On EventSource error, exponential backoff
1s → 2s → 4s → 8s (cap 30s); `Last-Event-ID` resumes after brief blips.

## URL structure

Served from `/pub/chat/` (public static); all routing is client-side and
hash-based (`/pub/chat/#atlas`, `/pub/chat/#atlas/t1234567890`). Hash routing
avoids server-side 404s for deep links — no catch-all server route needed.
`/auth/login?return_to=/pub/chat/%23atlas` returns to the right group after
login.

## webd changes

1. Remove `handleGroupsPage` / `handleChatPage` (HTMX pages) and their `GET /`,
   `GET /chat/{folder...}` routes — proxyd serves `/pub/chat/` statically.
2. Add the new `/api/*` endpoints above.
3. Grant-filter `GET /api/groups`: intersect `AllGroups()` with the caller's
   `X-User-Groups` (already provided by proxyd middleware).
4. Keep `webd/static/` for the slink widget until it's fully removed.

The slink HTML widget is superseded, but the slink API (`/slink/<token>` POST,
`/slink/stream` SSE) stays — it's the external surface used by the skill.

## Implementation phases

Each phase is an acceptance milestone: it ships working and testable.

1. **Scaffold + auth + group list** — `chatapp/` (Vite + React + Tailwind),
   `GET /api/me`, grant-filtered `GET /api/groups`, group rail renders + selects,
   401 → auth redirect, `make chat`.
2. **Thread list + basic chat** — thread list from `/api/groups/<folder>/topics`,
   `GET .../events` SSE + `POST .../messages` in webd, chat pane (history + send
   - SSE receive), virtual scroll, typing indicator.
3. **Tool cards + platform badges + status** — `tool_call`/`tool_result` →
   collapsible ToolCard, platform badge on user messages, `GET .../status`
   polling → agent status indicator, unread dots.
4. **Group + thread creation** — new-thread flow (topic POST), new-group modal
   (POST to onbod), group settings panel, `⌘K` command palette.
5. **Polish + mobile** — mobile drawer + bottom nav, keyboard shortcuts,
   dark/light toggle, `Last-Event-ID` resumption, accessibility pass
   (`aria-live` message list, keyboard-reachable controls, reduced-motion).

## What this is NOT

- Not a replacement for Telegram/Discord/WhatsApp. Those adapters stay; this
  is one more channel feeding the same agents.
- Not a multi-agent orchestration dashboard (that is `/dash/` — routing rules,
  grants, admission queues). This is the user chat surface.
- Not the slink public widget. Slink remains for anonymous/external access via
  a share token; this app is for authenticated users who belong to the instance.
