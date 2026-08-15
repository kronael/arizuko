-- Z5: 0005-network-rules.sql seeded its two default rows with CURRENT_TIMESTAMP,
-- SQLite's wall clock at MIGRATION-RUN time. created_at is part of network_rules'
-- resreg Export projection, and resreg.Checksum hashes that projection verbatim, so
-- two independently-bootstrapped routd.db files whose migrations crossed a wall-clock
-- second got permanently unequal checksums from identical operator intent.
--
-- Normalise the seed to a fixed literal, matching 0022-seed-operator-grant-option.sql
-- (the other "seed a default row" migration, which got this right). Scoped to the two
-- system-seeded rows by their exact PK plus created_by='system', so an operator who
-- re-added either target keeps their own stamp.
--
-- The fix is here and NOT in Checksum: the CAS hash must stay sha256 of the exact
-- EmitYAML bytes Export produces (one renderer, spec 5/8), so the cure for a
-- non-deterministic seed is a deterministic seed, not a weaker hash. Diff already
-- ignores StampedFields because it answers a different question (did INTENT change).
UPDATE network_rules SET created_at = '2026-08-04T00:00:00Z'
 WHERE folder = '' AND created_by = 'system'
   AND target IN ('anthropic.com', 'api.anthropic.com');
