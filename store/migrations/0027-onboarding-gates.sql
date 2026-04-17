-- Gated onboarding: track which gate admitted a user and when they queued.
ALTER TABLE onboarding ADD COLUMN gate TEXT;
ALTER TABLE onboarding ADD COLUMN queued_at TEXT;
