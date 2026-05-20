---
status: shipped
---

> Shipped 2026-04-22 (commits `d94c0af`, `847cb4a`, `44122e1`):
> `fetch_history` + `inspect_messages` registered in `ipc/ipc.go:897-953`;
> FetchHistory implemented on discd, bskyd, mastd, reditd, emaid, teled,
> linkd. whapd deferred (Baileys unreliable).

# History Fetch (channel-first) + Inspect Tools

Two tools with distinct audiences:

| Tool               | Source        | Audience           | Purpose                               |
| ------------------ | ------------- | ------------------ | ------------------------------------- |
| `fetch_history`    | channel (API) | agent (reasoning)  | conversation context, backfill depth  |
| `inspect_messages` | local DB      | agent (ops / logs) | outbound audit, routing, errored rows |

## Why two tools

1. **What was actually said in the conversation** — the channel/platform
   is authoritative. Local DB may be missing rows (adapter was offline,
   WhatsApp LID translation failing, etc.).
2. **What passed through arizuko** — inbound rows, outbound audit,
   `errored=1` flags, bot IDs. For this, local DB is authoritative.

`inspect_messages` and `fetch_history` serve different audiences;
conflating them hides the platform gap and mixes ops data into reasoning
context.

## `fetch_history` — channel-first

Agent calls `fetch_history(jid, before, limit)`. Gateway resolves the
adapter from JID prefix and proxies to `<adapter>/v1/history?
jid=…&before=…&limit=…`. Adapter hits platform API, writes rows into
local DB (write-through cache), returns them.

Fallback: if adapter is offline or platform errors, gateway returns
`{source: "cache"}` with whatever local DB has. Agent sees the source
tag.

Telegram is 24h-limited (`source:"platform", cap:"24h"`). WhatsApp is
excepted (Baileys unreliable) — returns local DB only with
`source:"cache-only"`.

## `inspect_messages` — local DB, operational

Used for:

- answering "what did I send the last time" (outbound audit)
- debugging (`errored=1` rows, session boundaries, bot_message flag)
- /logs-style skills

## Non-goals

- Media re-download on fetch (use existing media enricher).
- Cross-platform history merge. One JID, one platform.
