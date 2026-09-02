-- The hold row's effect chooses whether the agent WAITS for the verdict, and the
-- suspended call has to remember which it was: the blocking waiter runs the call
-- itself when the approval lands, so resolveHoldTx must NOT also enqueue the
-- re-issue trigger, or the call would run twice. A waiter that gives up flips the
-- row back to 'async', which restores that trigger for a late approval.
ALTER TABLE pending_actions ADD COLUMN mode TEXT NOT NULL DEFAULT 'async';
