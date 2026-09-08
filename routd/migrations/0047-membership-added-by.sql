-- acl_membership.added_by becomes NOT NULL, defaulting to the empty string.
--
-- The column is the resource's own row filter: `acl_membership` excludes the
-- edges pairing consented to (`added_by = 'pairing'`) from the manifest-visible
-- projection, because a consented identity link is runtime state and not
-- declarative config. That filter is one declaration the export, the wholesale
-- delete and the scoped delete all honour, and on the object path a filter is a
-- CONJUNCTION of attribute comparisons with no OR in it (objstore.Query.Where).
--
-- `(added_by IS NULL OR added_by != 'pairing')` has no such form. `added_by <>
-- 'pairing'` does, and it is the same answer for every row that carries a value.
-- Measured on all three live instances: 84 edges, every one of them attributed,
-- not a single NULL. This migration makes that structural instead of true by
-- luck, so a later row cannot fall out of its own resource's projection —
-- invisible to export, and untouched by the apply that should have replaced it.
--
-- The empty string is what NULL said: the writer recorded no actor. It is a
-- value both shapes can compare, where absence is not.
--
-- Rebuild rather than ALTER, because SQLite cannot add NOT NULL in place (same
-- pattern as 0032). Column order and the child index are preserved verbatim.

CREATE TABLE acl_membership_new (
  child       TEXT NOT NULL,
  parent      TEXT NOT NULL,
  added_by    TEXT NOT NULL DEFAULT '',
  added_at    TEXT NOT NULL,
  PRIMARY KEY (child, parent)
);

INSERT INTO acl_membership_new (child, parent, added_by, added_at)
SELECT child, parent, COALESCE(added_by, ''), added_at FROM acl_membership;

DROP TABLE acl_membership;
ALTER TABLE acl_membership_new RENAME TO acl_membership;

CREATE INDEX acl_membership_by_child ON acl_membership(child);
