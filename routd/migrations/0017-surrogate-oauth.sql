-- surrogate OAuth (spec 5/15): capability-credential columns on routd's secrets
-- table. A pasted PAT leaves all four NULL and the broker reads `value`
-- unchanged; a "Connect <provider>" OAuth row (dashd /dash/me/connections)
-- populates them and the broker refreshes `value` near expiry. refresh_val is
-- sealed with the same AES-256-GCM keyring as `value`. One row per
-- (scope_kind='user', scope_id=user_sub, key=<provider secret_key>).
ALTER TABLE secrets ADD COLUMN provider    TEXT;
ALTER TABLE secrets ADD COLUMN refresh_val BLOB;
ALTER TABLE secrets ADD COLUMN expires_at  DATETIME;
ALTER TABLE secrets ADD COLUMN scope_list  TEXT;
