-- installed_packages mirrors routd migration 0020 into the shared schema
-- library. routd.db owns the table (spec 5/28); store.Migrate carries every
-- table routd.db and onbod.db own, and this one was never mirrored across —
-- so resreg.Export(SubsystemRoutd) could not scan the resource that declares
-- it. Definition is identical to routd/migrations/0020-installed-packages.sql.
CREATE TABLE IF NOT EXISTS installed_packages (
    name         TEXT PRIMARY KEY,
    source       TEXT NOT NULL,
    revision     TEXT NOT NULL,
    manifest     TEXT NOT NULL DEFAULT '{}',   -- JSON: asset-kind -> [owned identities]
    asset_hashes TEXT NOT NULL DEFAULT '{}',   -- JSON: asset-id -> content hash
    installed_at TEXT NOT NULL
);
