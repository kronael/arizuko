-- spec 4/R: WITH GRANT OPTION. grant_option=1 → the holder may re-delegate this
-- acl row (or a subset). The delegation axis, orthogonal to the action lattice's
-- read/write coverage. Default 0 (no re-delegation) is safe for every existing row.
-- Mirrored verbatim in store/migrations (messages.db) so a Store opened against
-- either DB has the column (same pattern as 0007-acl / 0052-acl-unified).
ALTER TABLE acl ADD COLUMN grant_option INTEGER NOT NULL DEFAULT 0;
