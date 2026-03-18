-- Grant override rules per group (action grants)
CREATE TABLE IF NOT EXISTS grant_rules (
  folder TEXT NOT NULL PRIMARY KEY,
  rules  TEXT NOT NULL  -- JSON string[]
);
