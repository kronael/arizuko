-- inbound_pending — one row per inbound message routd did not accept, held by
-- the adapter until it does.
--
-- Only adapters that CONSUME the platform event on arrival keep this table.
-- slakd answers Slack 200 before routd is called (Slack demands the ack inside
-- 3s and redelivers on non-2xx, so deferring the ack turns one slow routd into
-- several copies of the message); discd's gateway hands each event over once.
-- Neither platform will offer the message again, so a failed routd call used to
-- lose it with one ERROR line as the trace (BUGS L25).
--
-- teled, bskyd, reditd, linkd and emaid advance their cursor ONLY after routd
-- accepts, so the platform is already their queue and they must not wire this:
-- two queues for one message is two things to drift.
--
-- id is the adapter's stable message id — the same value routd stores as the
-- messages PK, so a re-delivery of a row routd already has is an INSERT OR
-- IGNORE no-op rather than a duplicate.
CREATE TABLE inbound_pending (
  id              TEXT PRIMARY KEY,
  payload         TEXT NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  first_seen_at   TEXT NOT NULL,
  next_attempt_at TEXT NOT NULL,
  last_error      TEXT
);

CREATE INDEX inbound_pending_due ON inbound_pending(next_attempt_at);
