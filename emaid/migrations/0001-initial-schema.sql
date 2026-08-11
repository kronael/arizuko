-- emaid.db initial schema. emaid owns this DB and its own migration
-- sequence (service="emaid"). The tables hold adapter-internal email
-- threading rows: msg_id -> thread lookups for inbound mail.
--
-- IF NOT EXISTS is load-bearing: live instances created both tables
-- with an inline CREATE TABLE before migrations existed, and hold no
-- migrations table. This migration must adopt such a file as version 1
-- without an error and without a change to its rows. Do not remove the
-- clause; later migrations (0002+) use the normal bare form.

CREATE TABLE IF NOT EXISTS email_threads (
  thread_id    TEXT PRIMARY KEY,
  from_address TEXT NOT NULL,
  root_msg_id  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS email_msg_ids (
  msg_id    TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL
);
