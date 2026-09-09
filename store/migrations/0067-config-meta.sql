-- Spec 5/8: config_version table for optimistic locking on YAML apply.
--
-- Single-row table. Apply bumps it once per tx; CAS comparison happens
-- inside BEGIN IMMEDIATE.
--
-- The bootstrap value is a plain zero. It counted the config rows a database
-- already held, so that a first apply against a populated database did not
-- always reject with "version 0 != exported 0" — and every table it counted
-- (groups, acl, acl_membership, routes, web_routes, scheduled_tasks,
-- network_rules, proxyd_routes) is an objstore entity rather than a table this
-- chain creates: the driver makes each one, from the declaration that sits
-- beside the code writing it (store.EnsureEntities, spec 6/37 step 7). This
-- chain builds a FRESH pre-split monolith, so every one of those counts was
-- zero anyway; the value is the same and the statement no longer names eight
-- tables that are not there. `secrets` was excluded from the count for its own
-- reason, which still holds: out-of-band blob rotation must not invalidate a
-- pending apply.

CREATE TABLE config_meta (
  version INTEGER NOT NULL DEFAULT 0
);

INSERT INTO config_meta (version) VALUES (0);
