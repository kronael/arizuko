-- At most one signing key may be active at a time (the sole signer). A partial
-- unique index makes concurrent Rotate() racing two active rows a constraint
-- violation rather than split signing authority at the trust root.
-- Spec: specs/5/1-auth-standalone.md "exactly one active key".
CREATE UNIQUE INDEX idx_signing_keys_one_active ON signing_keys(active) WHERE active = 1;
