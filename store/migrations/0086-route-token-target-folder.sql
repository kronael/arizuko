-- Schema parity with routd migration 0035 so store's route_tokens read/write
-- paths (webd, proxyd, dashd, CLI, tests) see the same columns routd owns.
-- Rationale is in the routd twin.

ALTER TABLE route_tokens ADD COLUMN target_folder TEXT;
