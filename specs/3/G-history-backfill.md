---
status: deferred
---

# History Fetch (channel-first) + Inspect Tools

Rename and split today's single `get_history` into two distinct tools
with distinct audiences:

| Tool               | Source        | Audience           | Purpose                               |
| ------------------ | ------------- | ------------------ | ------------------------------------- |
| `fetch_history`    | channel (API) | agent (reasoning)  | conversation context, backfill depth  |
| `inspect_messages` | local DB      | agent (ops / logs) | outbound audit, routing, errored rows |

## Why split

`get_history` today reads the local DB. That conflates two jobs:

1. **What was actually said in the conversation** — for which the
   channel/platform is authoritative. Local DB may be missing rows
   (adapter was offline, WhatsApp LID translation failing, etc.).
2. **What passed through arizuko** — inbound rows, outbound audit,
   `errored=1` flags, bot IDs. For this, local DB is authoritative.

Calling local DB "history" hides the platform gap and mixes ops data
into reasoning context.

## `fetch_history` — channel-first

Agent calls `fetch_history(jid, before, limit)`. Gateway resolves the
adapter from JID prefix and proxies to `<adapter>/v1/history?
jid=…&before=…&limit=…`. Adapter hits platform API, writes rows into
local DB (write-through cache), returns them.

Fallback: if adapter is offline or platform errors, gateway returns
`{source: "cache"}` with whatever local DB has. Agent sees the source
tag.

Ship Discord first (clean API, unlimited depth). Then Bluesky, Mastodon,
Reddit, Email. Telegram is 24h-limited — wrap with `source:"platform",
cap:"24h"`. WhatsApp is excepted (Baileys unreliable) — returns local
DB only with `source:"cache-only"`.

## `inspect_messages` — local DB, operational

Same shape as today's `get_history`. Used for:

- answering "what did I send the last time" (outbound audit)
- debugging (`errored=1` rows, session boundaries, bot_message flag)
- /logs-style skills

No rename-and-flip migration needed in agent skills — `get_history`
stays as a thin alias for `inspect_messages` for one release, then the
alias is removed.

## Decision

Deferred. Ship the Discord proof of `fetch_history` first; rename only
after the pattern is proven on one adapter.

## Non-goals

- Media re-download on fetch (use existing media enricher).
- Cross-platform history merge. One JID, one platform.
