-- route-token link context (spec 5/W § link context) — schema parity with
-- routd migration 0018 so store's route_tokens read/write paths (webd tests,
-- FS-mounted writers) see the same shape. NULL = pre-context token.
ALTER TABLE route_tokens ADD COLUMN context TEXT;
