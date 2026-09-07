-- Persistent channel adapter registry for cross-process URL lookup. The
-- onboarding state machine this file also created is an objstore entity now,
-- made by the driver from the declaration beside the code that writes it
-- (store.EnsureEntities, spec 6/37 step 7).
CREATE TABLE IF NOT EXISTS channels (
  name         TEXT PRIMARY KEY,
  url          TEXT NOT NULL,
  jid_prefixes TEXT,
  capabilities TEXT
);
