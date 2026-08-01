-- identity_codes was created by 0004 for a short-lived /auth/link-code flow
-- that was never built: no Go code reads or writes the table, and it holds zero
-- rows on every deployed instance. The pairing carrier that DID get built is
-- onboarding.token (spec 5/31 extracts it), not this.
--
-- Dropped additively rather than by editing 0004, which has already run.
DROP TABLE IF EXISTS identity_codes;
