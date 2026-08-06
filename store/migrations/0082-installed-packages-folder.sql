-- Mirrors routd migration 0031 into the shared schema library: installed_packages
-- is re-keyed on (folder, name), folder '' meaning instance-wide (spec 5/28
-- composition, BUGS F30). Definition identical to
-- routd/migrations/0031-installed-packages-folder.sql.
--
-- store's stream never runs against a live routd.db (store.OpenRoutd skips
-- migrations by design — routd owns that file), so this exists so OpenMem test
-- fixtures and resreg.Export(SubsystemRoutd) see the same shape production does.
-- Migrations are append-only, so 0081 still creates the old shape first and this
-- file rebuilds it; the end state is what matters.
CREATE TABLE installed_packages_new (
    folder       TEXT NOT NULL DEFAULT '',    -- '' = instance-wide
    name         TEXT NOT NULL,
    source       TEXT NOT NULL,
    revision     TEXT NOT NULL,
    manifest     TEXT NOT NULL DEFAULT '{}',   -- JSON: asset-kind -> [owned identities]
    asset_hashes TEXT NOT NULL DEFAULT '{}',   -- JSON: asset-id -> content hash
    installed_at TEXT NOT NULL,
    PRIMARY KEY (folder, name)
);

INSERT INTO installed_packages_new (folder, name, source, revision, manifest, asset_hashes, installed_at)
SELECT '', name, source, revision, manifest, asset_hashes, installed_at FROM installed_packages;

DROP TABLE installed_packages;

ALTER TABLE installed_packages_new RENAME TO installed_packages;
