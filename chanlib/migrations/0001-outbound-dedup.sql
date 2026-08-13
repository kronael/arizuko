-- outbound_dedup — one row per outbound delivery the adapter was asked to make,
-- keyed on routd's idempotency key (the outbound message row id).
--
-- routd re-sends a row that stays `pending` (its 30s sweep), so the adapter
-- receives the SAME key more than once by design. Without a record the second
-- delivery posts a second platform message. The record is written BEFORE the
-- platform call, so a lost response leaves `inflight` — an outcome the adapter
-- cannot resolve and must not guess (BUGS D5).
--
-- state: 'inflight' = the platform call was started and its outcome is unknown.
--        'sent'     = the platform accepted it; platform_id is its message id.
CREATE TABLE outbound_dedup (
  idempotency_key TEXT PRIMARY KEY,
  chat_jid        TEXT NOT NULL,
  state           TEXT NOT NULL,
  platform_id     TEXT,
  created_at      TEXT NOT NULL,
  settled_at      TEXT
);

CREATE INDEX outbound_dedup_created ON outbound_dedup(created_at);
