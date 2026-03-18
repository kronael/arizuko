-- Onboarding state machine and channel adapter registry

CREATE TABLE IF NOT EXISTS onboarding (
  jid         TEXT PRIMARY KEY,
  status      TEXT NOT NULL,
  sender      TEXT,
  channel     TEXT,
  world_name  TEXT,
  prompted_at TEXT,
  created     TEXT NOT NULL
);

-- Persistent channel adapter registry for cross-process URL lookup
CREATE TABLE IF NOT EXISTS channels (
  name         TEXT PRIMARY KEY,
  url          TEXT NOT NULL,
  jid_prefixes TEXT,
  capabilities TEXT
);
