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
-- Each UPDATE is a pure re-rendering: it preserves the instant exactly, so the
-- final state cannot skip or replay a row the pre-migration state did not
-- already skip. A PARTIAL state can. Padding a cursor makes it shorter than the
-- unpadded rows of its own second, so cursors normalised while rows are not
-- re-feeds consumed messages: 28 on krons, 27 on marinade, 33 on sloth, measured
-- against copies of the live files. db_utils runs the whole file inside one
-- BEGIN EXCLUSIVE, so no reader ever sees that state — and the rows go first
-- here, which measured no flip either way, for anyone running these by hand.
--
-- Scoped by GLOB to the RFC3339-with-Z shapes and nothing else. Two krons rows
-- carry a hand-inserted `2026-04-02 12:38:47` (space separator, no zone); they do
-- not match, and padding them would invent a zone they never had.
--
-- ONLY THE ROWS ARE HERE NOW, and the order this file's comment argues is now
-- enforced across two tools instead of stated inside one. `chats` and `sessions`
-- are objstore entities and their cursors are attributes, so the two UPDATEs
-- that padded them cannot run against a table of three columns.
-- `arizuko migrate-hot` pads them where it carries them, and it REFUSES to start
-- until every message row this file would pad is already padded — so cursors
-- can never be normalised ahead of their rows, which is the state that re-fed 28
-- consumed messages on krons. Rows before cursors is a precondition the tool
-- checks, not an order somebody has to preserve.
UPDATE messages
   SET timestamp = substr(timestamp, 1, 19) || '.' ||
       substr(CASE WHEN substr(timestamp, 20, 1) = '.'
                   THEN substr(timestamp, 21, length(timestamp) - 21)
                   ELSE '' END || '000000000', 1, 9) || 'Z'
 WHERE timestamp GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z'
   AND timestamp NOT GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z';
