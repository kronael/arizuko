-- Simplify onboarding: collapse 4-step flow to 2-step (awaiting_message → pending).
-- Move any in-progress records to the new initial state.
UPDATE onboarding SET status = 'awaiting_message', prompted_at = NULL
  WHERE status IN ('awaiting_world', 'awaiting_building', 'awaiting_room');
