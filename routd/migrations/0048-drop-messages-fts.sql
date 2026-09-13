-- 0048 — drop the FTS5 index over messages and its three triggers.
--
-- Ranked search is an in-memory BM25 index inside routd, built from the
-- `messages` entity at boot and fed by routd's own writes (spec 6/42 §8,
-- routd/msgsearch). Nothing reads this index.
--
-- It is a file of its own because a frozen chain cannot unsay a declaration:
-- 0001 declares the virtual table and the three triggers, so every routd.db
-- born after this release would carry them, and on a database whose `messages`
-- is the entity those triggers fire on every append to fill an index with
-- nobody on the other end.
--
-- DROP TABLE on an fts5 table takes its shadow tables with it, which is where
-- the index's own bytes are.

DROP TRIGGER IF EXISTS messages_fts_ai;
DROP TRIGGER IF EXISTS messages_fts_au;
DROP TRIGGER IF EXISTS messages_fts_ad;
DROP TABLE IF EXISTS messages_fts;
