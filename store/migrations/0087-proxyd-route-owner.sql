-- Mirror of routd/migrations/0038-proxyd-route-owner.sql. proxyd_routes lives in
-- routd.db; this keeps the store schema library (and every store.OpenMem test
-- DB) in step, exactly as 0072 mirrored redirect_to. See routd/0038 for why the
-- backfill is 'arizuko'.
ALTER TABLE proxyd_routes ADD COLUMN owner TEXT NOT NULL DEFAULT 'arizuko';
