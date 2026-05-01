-- Account linking: auth_users.linked_to_sub points at the canonical sub
-- when this row is a *linked* identity rather than canonical itself.
-- NULL = canonical (the row's own sub IS the account identifier).
-- Non-NULL = linked; sessions for this sub resolve to linked_to_sub at
-- JWT mint time. Single resolve point — downstream sees only canonical.

ALTER TABLE auth_users ADD COLUMN linked_to_sub TEXT;

CREATE INDEX IF NOT EXISTS idx_auth_users_linked_to_sub
  ON auth_users(linked_to_sub) WHERE linked_to_sub IS NOT NULL;
