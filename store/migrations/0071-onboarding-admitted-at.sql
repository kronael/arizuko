-- Gated onboarding: stamp when a row was admitted (status→approved) so the
-- per-gate daily limit counts admissions on the day they happened, not the day
-- they queued. Backlog queued on a prior day no longer evades today's quota.
ALTER TABLE onboarding ADD COLUMN admitted_at TEXT;
