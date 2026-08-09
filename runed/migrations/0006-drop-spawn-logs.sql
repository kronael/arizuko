-- spawn_logs was created for per-spawn output lines and never got a writer: an
-- exhaustive scan finds the name only in comments, never in an INSERT or a
-- SELECT. Nothing read it either, so no reader has to be redirected anywhere.
--
-- Dropped like its sibling mcp_tokens from the same initial schema (0003). An
-- empty table is not free: it is the only thing a schema reader sees, and it
-- promises that a spawn's output is queryable here when it is not.
DROP TABLE IF EXISTS spawn_logs;
