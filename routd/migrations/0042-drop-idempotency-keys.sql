-- Drop idempotency_keys — a replay ledger with no writer.
--
-- The REST turn face was its only writer, and that face is deleted. What
-- remained was an hourly DELETE against an always-empty table. A table
-- nothing writes is a schema claim the code does not make.
--
-- No data loss: every live instance holds zero rows.
DROP INDEX IF EXISTS idx_idempotency_expiry;
DROP TABLE IF EXISTS idempotency_keys;
