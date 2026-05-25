-- 0065 — drop orphaned shared-DB tables (no live writers since v0.28.0)
--
-- channels:       persistent adapter registry from 2026-03-18. Writer
--                 (onbod's INSERT) removed in v0.28.0 (commit 3d0c234,
--                 2026-04-15). Reader in dashd was orphaned. Production
--                 registry is the in-memory chanreg (specs/4/1-channel-protocol.md).
-- grants:         legacy permission table (migration 0005). Superseded
--                 by unified acl + acl_membership (specs/4/9-acl-unified.md).
--                 Zero live readers/writers; only the 0042 typed-JID
--                 migration touched the table for type changes.
-- email_threads:  shared-schema email thread index (migration 0001).
--                 emaid uses its own emaid.db with a different shape
--                 (thread_id, from_address, root_msg_id) per
--                 specs/5/U-genericization.md. The shared version was
--                 never written to.
--
-- Dropping is safe because no production code reads or writes any of
-- these. dashd's three reads of `channels` are removed in the same
-- commit to close the orphaned-reader gap. See bugs.md (2026-05-25).

DROP TABLE IF EXISTS channels;
DROP TABLE IF EXISTS grants;
DROP TABLE IF EXISTS email_threads;
