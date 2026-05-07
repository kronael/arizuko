---
name: recall-messages
description: >
  Use when asked what someone said, what was discussed, or to find a past
  conversation. Searches chat message history, not facts/diary.
user-invocable: true
arg: <question>
---

# Recall Messages

Search chat message history for past conversations ("what did X say",
"did we discuss Y", decisions made in conversation).
For stored knowledge (facts, diary, episodes) use `/recall-memories`.

## Protocol

1. Fetch history. Pick the right tool for the slice:
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
