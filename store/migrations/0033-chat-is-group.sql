-- Re-add chats.is_group dropped in 0023. Phase D of specs/7/35.
-- Phase C's secrets resolver gates user-scope secret injection on the
-- single-user predicate (Chat.IsSingleUser); without this column there
-- is no way to distinguish a DM from a group chat at spawn time.
--
-- Default 0 (not group). Adapters set the correct value on the next
-- inbound for any chat_jid; rows that never see another inbound stay
-- conservative — user secrets simply don't inject.
ALTER TABLE chats ADD COLUMN is_group INTEGER NOT NULL DEFAULT 0;
