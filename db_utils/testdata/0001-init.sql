CREATE TABLE announcements (
  service    TEXT NOT NULL,
  version    INTEGER NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (service, version)
);
