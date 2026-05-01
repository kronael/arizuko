# 084 — get_thread MCP tool

`get_thread(chat_jid, topic, [limit, before])` returns local-DB rows
scoped to one (chat_jid, topic) thread. Use it when a single chat
fans out into per-topic conversations — Telegram forum topics, web
chat topics — and you want one thread, not the whole chat.

Sibling distinctions:

- `get_thread` — one thread inside a chat (chat_jid + topic)
- `inspect_messages` — whole chat (chat_jid only), DB truth
- `fetch_history` — whole chat, platform-truth (adapter-fetched)

Returns `{messages, count, oldest, source: "local-db"}`. Same
tier-gating as `inspect_messages`: non-root callers can only query
a chat_jid that routes to their own folder.
