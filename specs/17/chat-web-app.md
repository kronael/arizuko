---
status: draft
---

# Chat web app

A React/Vite SPA at `/pub/chat/` — the primary human-facing interface to
arizuko, replacing the HTMX scaffolding in `webd/pages.go`.

## Problem

The HTMX chat pages are two separate pages: a groups grid and a per-group
chat. Switching groups navigates away. There is no thread list, no sidebar,
no multi-group live view, and no path to group creation. It does not scale
past a handful of groups.

## Decisions

- **Three-panel shell** (group rail → thread list → chat pane), collapsing
  to one pane with a nav drawer on mobile. A group is one agent; threads are
  topics in that agent's space. Messages from Telegram/Discord/WhatsApp
  appear here too with a platform badge — the web is another channel, not a
  privileged one.
- **Hash routing** (`/pub/chat/#atlas/t1234`), served as a static page from
  the existing `/pub` tree. Client-side routing under a hash needs no
  catch-all server route and cannot 404 on a deep link.
- **Built output is committed** to `template/web/pub/chat/` (small,
  deterministic) so instances need no Node runtime at deploy time.
- **Server data lives in the query cache, never in the UI store.** Only
  navigation and ephemeral UI state (selected folder/topic, mobile panel,
  per-group agent status) are client state. Two stores for one fact drift.
- **New authenticated SSE endpoint**, not a reworked token stream. The
  existing `/chat/<token>` route-token surface stays as the external,
  share-by-link path ([5/W-webhook-routes](../5/W-webhook-routes.md)); the
  app gets a JWT-gated per-group stream. Different auth boundary, different
  endpoint.
- **Not the operator dashboard.** Routing rules, grants, and admission
  queues stay in `/dash/` (phase [7/](../7/)). This is the user chat
  surface only.

## Data model

- **Group** = folder = one agent: `{folder, name, description, status}`,
  status derived from runed container state, description from the group's
  persona frontmatter.
- **Thread** = topic: `{topic_id, label, last_message_at}`; label is the
  head of the first user message, stored at creation.
- **Message** = `{id, role, content, topic, platform, ts, reply_to?}`.
  Agent messages render as Markdown, user messages as plain text.
- **Tool call** = paired `tool_call` + `tool_result` frames keyed by
  `call_id`, rendered as one collapsible card.

## API surface

Existing `/api/groups`, `/api/groups/<folder>/topics` and `.../messages`
stay, but `GET /api/groups` must be intersected with the caller's grant
scope (proxyd already supplies the caller's groups). New: `/api/me`, a
per-group `GET .../status`, an authenticated `GET .../events` SSE stream, a
`POST .../messages` send, topic creation, and a group-creation POST that
routes to onbod's `SetupGroup` — visible only to callers whose grants allow
it.

The SSE frame vocabulary is the one `webd/hub.go` already emits (`message`,
`typing`, `tool_call`, `tool_result`) — no new protocol. The chat pane holds
one EventSource for the selected group+topic and closes it on switch;
background groups poll for unread counts rather than holding open streams.
Reconnect is exponential backoff with `Last-Event-ID` resumption — which is
best-effort until [16/1-durable-turn-stream](../16/1-durable-turn-stream.md)
lands, since the hub drops slow clients silently today
(`webd/hub.go:70`).

## Phasing

Each phase ships working and testable: (1) scaffold + auth + group list;
(2) thread list + chat with SSE send/receive; (3) tool cards, platform
badges, agent status; (4) group and thread creation, settings; (5) mobile,
keyboard shortcuts, accessibility pass.

## What this is not

- Not a replacement for the chat adapters — they stay, feeding the same
  agents.
- Not the operator dashboard (`/dash/`).
- Not the public share widget — that is the route-token surface, for
  external and anonymous access.
