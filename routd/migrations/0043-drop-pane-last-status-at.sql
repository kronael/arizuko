-- pane_sessions is an objstore entity and carries no last_status_at attribute,
-- so there is no column left to drop. The attribute had no writer and no reader
-- and every live instance held only NULLs in it, which is why nothing carries
-- it forward: `arizuko migrate-store` reads the six fields the entity keeps.
SELECT 1;
