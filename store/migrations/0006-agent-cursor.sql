-- Per-group agent cursor replacing JSON blob in router_state

ALTER TABLE registered_groups ADD COLUMN agent_cursor TEXT;
