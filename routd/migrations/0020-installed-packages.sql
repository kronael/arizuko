-- installed-package record (spec 5/28): one row per package installed on this
-- instance. Generalises 5/20's products.lock — the source + resolved immutable
-- revision a package was installed from, the manifest of identities the install
-- owns (route paths, acl keys, skill dirs, files, fragment names), and a
-- per-asset content hash. This is the lock that makes upgrade / remove /
-- dirty-detection specifiable (NOT per-row provenance). Written by
-- `arizuko packages install|remove`.
CREATE TABLE IF NOT EXISTS installed_packages (
    name         TEXT PRIMARY KEY,
    source       TEXT NOT NULL,
    revision     TEXT NOT NULL,
    manifest     TEXT NOT NULL DEFAULT '{}',   -- JSON: asset-kind -> [owned identities]
    asset_hashes TEXT NOT NULL DEFAULT '{}',   -- JSON: asset-id -> content hash
    installed_at TEXT NOT NULL
);
