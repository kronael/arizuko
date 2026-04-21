CREATE TABLE announcement_sent (
  service   TEXT NOT NULL,
  version   INTEGER NOT NULL,
  user_jid  TEXT NOT NULL,
  sent_at   TEXT NOT NULL,
  PRIMARY KEY (service, version, user_jid)
);
