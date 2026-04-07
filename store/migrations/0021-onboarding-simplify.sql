-- Simplify onboarding: collapse 4-step flow to 2-step (awaiting_message → pending).
-- Move any in-progress records to the new initial state, but skip JIDs that already have routes.
UPDATE onboarding SET status = 'awaiting_message', prompted_at = NULL
  WHERE status IN ('awaiting_world', 'awaiting_building', 'awaiting_room')
  AND jid NOT IN (SELECT DISTINCT jid FROM routes);
-- Clean up onboarding records for JIDs that already have routes.
DELETE FROM onboarding WHERE jid IN (SELECT DISTINCT jid FROM routes);
