---
name: recall-messages
description: >
  Search live chat message history via `find_messages` (content search),
  `inspect_messages`, `get_thread`, or `fetch_history` MCP tools. USE for
  "what did X say", "what was discussed", "find that conversation",
  scrollback search. NOT for stored knowledge — facts/diary/episodes
  (use recall-memories).
user-invocable: true
arg: <question>
---

# Recall Messages

## Protocol

1. Fetch history. Pick the right tool for the slice:
   - **Search by content** (you know what was said, not where):
     `find_messages({ query: "<terms>", scope: "<chat_jid or folder>", limit: 50 })`.
     FTS5 syntax: `"exact phrase"`, `a OR b`, `a NOT b`, `prefix*`,
     `NEAR(a b, 5)`. Returns ranked snippets with `«match»` highlight.
   - **Whole chat**: `inspect_messages({ chat_jid: "<jid>", limit: 200 })`
   - **One thread** (chat fans out to per-topic threads — Telegram forum
     topics, web-chat topics): `get_thread({ chat_jid: "<jid>", topic: "<topic>" })`
   - **Platform-side backfill** (DB is empty after `reset_session`, or
     scrollback predates the agent): `fetch_history({ chat_jid: "<jid>",
     limit: 200 })` — calls the adapter, caches into the local DB.

   Write the result to `~/tmp/messages.json`.

2. Spawn Explore subagent:

   > Search `~/tmp/messages.json` for messages related to: `<question>`.
   > Return matching messages with sender, timestamp, and content.
   > Summarize what you found and how it relates.

3. Report findings. Do NOT fabricate matches or infer from partial text.

## Pagination

If no match in 200 messages, ask the user whether to go further back.
Each subsequent call uses `before: <oldest timestamp seen>`. For
`fetch_history`, the adapter pulls another page from the platform and
appends to the cache.
