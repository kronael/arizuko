-- Re-key installed_packages on (folder, name). Spec 5/28's composition blends an
-- ordered product mix per GROUP, and a per-group mix cannot key into an
-- instance-keyed table (BUGS F30). The alternative was a second, group-scoped
-- lock — the "second package manager" 5/28 forbids in its own /migrate
-- paragraph. One lock, correctly keyed.
--
-- folder = '' means INSTANCE-WIDE. That is not a new sentinel: network_rules
-- already ships composite (folder, target) with '' for a global rule
-- (store/migrations/0037-network-rules.sql). Every existing row IS instance-wide
-- by construction — `arizuko packages install` writes compose fragments, proxyd
-- routes and host files, none of which belong to a group — so the copy stamps ''
-- and today's installs keep resolving unchanged.
--
-- SQLite cannot ALTER a PRIMARY KEY, so this is a table rebuild. The
-- INSERT ... SELECT must carry EVERY row: this record is what `remove` reads to
-- find the routes, grants and files an install owns, so a row dropped here
-- orphans all of them silently. db_utils.Migrate already wraps the file in
-- BEGIN EXCLUSIVE/COMMIT — no transaction control here.
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
