CREATE TABLE announcements (
  service    TEXT NOT NULL,
  version    INTEGER NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);

CREATE TABLE announcement_sent (
  service   TEXT NOT NULL,
  version   INTEGER NOT NULL,
  user_jid  TEXT NOT NULL,
  sent_at   TEXT NOT NULL,
  PRIMARY KEY (service, version, user_jid)
);
