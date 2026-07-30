-- spec 4/R: WITH GRANT OPTION. grant_option=1 → the holder may re-delegate this
-- acl row (or a subset). Default 0 is safe for every existing row. Mirrors
-- routd/migrations/0021-acl-grant-option.sql verbatim (same acl schema, two DBs).
ALTER TABLE acl ADD COLUMN grant_option INTEGER NOT NULL DEFAULT 0;

-- role:operator is the root of every delegation chain (spec 4/R): (*, **) WITH
-- GRANT OPTION. Mirrors routd/migrations/0021-acl-grant-option.sql.
UPDATE acl SET grant_option = 1 WHERE principal = 'role:operator' AND action = '*';
