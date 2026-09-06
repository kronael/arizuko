-- L97: pad every stored timestamp to a fixed-width nine-digit fraction, so
-- lexicographic order over the column IS chronological order (core.TSLayout).
--
-- The three columns here are one mechanism: messages.timestamp is the subject of
-- two cursors, chats.agent_cursor (the dispatch feed) and sessions.observed_cursor
-- (the observe feed), and each cursor is a copy of some row's timestamp compared
-- back against the column with `>`. A stripped fraction makes that comparison
-- disagree with time inside one second — `.` is 0x2E, `Z` is 0x5A — and a cursor
-- only moves forward, so the row is skipped for good.
--
-- The code fix alone does not reach a deployed instance: 18 of 136 krons chats
-- hold a whole-second cursor written before it, and a nanosecond row arriving in
-- that same second still sorts behind the stored `...57Z`. The column and the
-- cursors have to be normalised together for the new layout to mean anything.
--
-- Order within this file does not matter and must not be relied on: db_utils
-- runs the whole file inside one BEGIN EXCLUSIVE, so no reader sees a column
-- normalised against a cursor that is not. Each UPDATE is a pure re-rendering —
-- it preserves the instant exactly — so the final state cannot skip or replay a
-- row that the pre-migration state did not already skip.
--
-- Scoped by GLOB to the RFC3339-with-Z shapes and nothing else. Two krons rows
-- carry a hand-inserted `2026-04-02 12:38:47` (space separator, no zone); they do
-- not match, and padding them would invent a zone they never had.
UPDATE messages
   SET timestamp = substr(timestamp, 1, 19) || '.' ||
       substr(CASE WHEN substr(timestamp, 20, 1) = '.'
                   THEN substr(timestamp, 21, length(timestamp) - 21)
                   ELSE '' END || '000000000', 1, 9) || 'Z'
 WHERE timestamp GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z'
   AND timestamp NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';

UPDATE chats
   SET agent_cursor = substr(agent_cursor, 1, 19) || '.' ||
       substr(CASE WHEN substr(agent_cursor, 20, 1) = '.'
                   THEN substr(agent_cursor, 21, length(agent_cursor) - 21)
                   ELSE '' END || '000000000', 1, 9) || 'Z'
 WHERE agent_cursor GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z'
   AND agent_cursor NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';

UPDATE sessions
   SET observed_cursor = substr(observed_cursor, 1, 19) || '.' ||
       substr(CASE WHEN substr(observed_cursor, 20, 1) = '.'
                   THEN substr(observed_cursor, 21, length(observed_cursor) - 21)
                   ELSE '' END || '000000000', 1, 9) || 'Z'
 WHERE observed_cursor GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z'
   AND observed_cursor NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';
