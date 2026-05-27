-- Spec 5/36: config_version table for optimistic locking on YAML apply.
--
-- Single-row table. Apply bumps it once per tx; CAS comparison happens
-- inside BEGIN IMMEDIATE. Bootstrap value counts existing config rows
-- so a fresh apply against a populated DB doesn't always reject with
-- "version 0 != exported 0". `secrets` is excluded from the count —
-- out-of-band blob rotation must not invalidate pending applies.

CREATE TABLE config_meta (
  version INTEGER NOT NULL DEFAULT 0
);

INSERT INTO config_meta (version)
  SELECT
    (SELECT COUNT(*) FROM groups)            +
    (SELECT COUNT(*) FROM acl)               +
    (SELECT COUNT(*) FROM acl_membership)    +
    (SELECT COUNT(*) FROM routes)            +
    (SELECT COUNT(*) FROM web_routes)        +
    (SELECT COUNT(*) FROM scheduled_tasks)   +
    (SELECT COUNT(*) FROM network_rules)     +
    (SELECT COUNT(*) FROM proxyd_routes)     +
    (SELECT COUNT(*) FROM onboarding_gates);
