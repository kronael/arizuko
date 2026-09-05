-- Drop pane_sessions.last_status_at — a column with no writer and no reader.
--
-- It was declared for a setStatus debounce whose Go side is deleted; the
-- column stayed, so the schema and the two schema pages described a debounce
-- the code does not perform.
--
-- No data loss: every live instance holds only NULLs in it.
ALTER TABLE pane_sessions DROP COLUMN last_status_at;
