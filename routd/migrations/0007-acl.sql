-- routd owns the unified ACL (spec 5/5 § Daemon ownership of /v1/*: routd owns
-- acl). Schema mirrors store/migrations/0052-acl-unified.sql verbatim so the
-- same evaluator (auth.AuthorizeWith / store.UserScopes / store.ListACL) reads
-- routd.db's own rows instead of sibling-reading gated's messages.db.
CREATE TABLE acl (
  principal   TEXT NOT NULL,
  action      TEXT NOT NULL,
  scope       TEXT NOT NULL,
  effect      TEXT NOT NULL DEFAULT 'allow',
  params      TEXT NOT NULL DEFAULT '',
  predicate   TEXT NOT NULL DEFAULT '',
  granted_by  TEXT,
  granted_at  TEXT NOT NULL,
  PRIMARY KEY (principal, action, scope, params, predicate, effect)
);
CREATE INDEX acl_by_principal_action ON acl(principal, action);
CREATE INDEX acl_by_scope             ON acl(scope);

CREATE TABLE acl_membership (
  child       TEXT NOT NULL,
  parent      TEXT NOT NULL,
  added_by    TEXT,
  added_at    TEXT NOT NULL,
  PRIMARY KEY (child, parent)
);
CREATE INDEX acl_membership_by_child ON acl_membership(child);
