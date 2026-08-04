-- Mirror of routd/migrations/0028-route-attribution.sql. routd owns `routes` in
-- the split and store.Migrate has no production caller left (only
-- tests/testutils), but the two sequences must still describe the same table or
-- every test on a store-migrated DB tests a schema that no longer ships.
-- Rationale for the columns lives in the routd file.
ALTER TABLE routes ADD COLUMN added_by  TEXT;
ALTER TABLE routes ADD COLUMN added_via TEXT;
