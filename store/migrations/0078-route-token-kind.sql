-- route-token kind (spec 5/31) — schema parity with routd migration 0026 so
-- store's route_tokens read/write paths (webd, proxyd, CLI, tests) see the same
-- shape. 'route' = delivery bearer, 'pair' = identity pairing.
ALTER TABLE route_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'route';
