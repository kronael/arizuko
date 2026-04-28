CREATE TABLE turn_results (
  folder       TEXT NOT NULL,
  turn_id      TEXT NOT NULL,
  session_id   TEXT,
  status       TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  PRIMARY KEY (folder, turn_id)
);
