-- Proactive interjection (spec 5/33). One chat-scoped runtime row: the
-- per-chat 24h cooldown for the proactive trigger. Mode is group business
-- state read from the group's CLAUDE.md frontmatter, NOT a column (single
-- source, no DB/file drift). Times are RFC3339Nano UTC TEXT.

CREATE TABLE chat_proactive (
  jid                     TEXT PRIMARY KEY,   -- the chat (chats.jid)
  proactive_last_fired_at TEXT                -- NULL = never fired
);
