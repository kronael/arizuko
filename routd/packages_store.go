package routd

// packages_store.go is the installed-package record (spec 5/28): CRUD over the
// installed_packages table. It generalises 5/20's products.lock — one row per
// installed package holding the source + resolved revision, the manifest of
// identities the install owns, and a per-asset content hash. The `packages
// install|remove` machinery reads/writes it; it is the basis for upgrade
// (diff new vs recorded manifest), remove (delete recorded identities), and
// dirty-detection (current hash vs recorded hash). NOT per-row provenance.

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// InstalledPackage is one installed_packages row. Manifest maps an asset kind
// (skills / compose_fragment / proxyd_route / acl / group_seed) to the
// identities this install owns; AssetHashes maps an asset id to the content
// hash written, so upgrade can detect a locally-edited (dirty) asset.
//
// Folder is the half of the key spec 5/28's composition needs: a group blends an
// ordered product mix, so the lock has to name WHICH group. InstanceWide ("") is
// the sentinel for a package installed against the whole instance — every row
// the CLI writes today, and the same convention network_rules uses for a global
// egress rule.
type InstalledPackage struct {
	Folder      string
	Name        string
	Source      string
	Revision    string
	Manifest    map[string][]string
	AssetHashes map[string]string
	InstalledAt string
}

// InstanceWide is installed_packages.folder for a package that belongs to the
// instance rather than to one group. `arizuko packages install` writes compose
// fragments, proxyd routes and host files — none of which belong to a group — so
// it is the only value the CLI produces.
const InstanceWide = ""

const installedPackageCols = `folder, name, source, revision, manifest, asset_hashes, installed_at`

// PutInstalledPackage upserts a package's record (install and upgrade). Manifest
// and AssetHashes serialise to JSON; nil maps become `{}` so a NULL never lands.
func (d *DB) PutInstalledPackage(p InstalledPackage) error {
	if p.Manifest == nil {
		p.Manifest = map[string][]string{}
	}
	if p.AssetHashes == nil {
		p.AssetHashes = map[string]string{}
	}
	manifest, err := json.Marshal(p.Manifest)
	if err != nil {
		return err
	}
	hashes, err := json.Marshal(p.AssetHashes)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT INTO installed_packages(`+installedPackageCols+`)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(folder, name) DO UPDATE SET source=excluded.source, revision=excluded.revision,
			manifest=excluded.manifest, asset_hashes=excluded.asset_hashes, installed_at=excluded.installed_at`,
		p.Folder, p.Name, p.Source, p.Revision, string(manifest), string(hashes), p.InstalledAt)
	return err
}

// InstalledPackage returns one record by its full key; ok=false when absent.
// Pass InstanceWide for folder to reach a package installed instance-wide.
func (d *DB) InstalledPackage(folder, name string) (InstalledPackage, bool, error) {
	p, err := scanInstalledPackage(d.db.QueryRow(`SELECT `+installedPackageCols+`
		FROM installed_packages WHERE folder=? AND name=?`, folder, name))
	if errors.Is(err, sql.ErrNoRows) {
		return InstalledPackage{}, false, nil
	}
	if err != nil {
		return InstalledPackage{}, false, err
	}
	return p, true, nil
}

// InstalledPackages lists every record, instance-wide rows first (folder ''
// sorts before any group), then by name.
func (d *DB) InstalledPackages() ([]InstalledPackage, error) {
	rows, err := d.db.Query(`SELECT ` + installedPackageCols + `
		FROM installed_packages ORDER BY folder, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstalledPackage
	for rows.Next() {
		p, err := scanInstalledPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteInstalledPackage removes a record (remove). ok=false when absent.
func (d *DB) DeleteInstalledPackage(folder, name string) (bool, error) {
	res, err := d.db.Exec("DELETE FROM installed_packages WHERE folder=? AND name=?", folder, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// rowScanner is satisfied by both *sql.Row (QueryRow) and *sql.Rows (Query).
type rowScanner interface{ Scan(...any) error }

func scanInstalledPackage(s rowScanner) (InstalledPackage, error) {
	var p InstalledPackage
	var manifest, hashes string
	if err := s.Scan(&p.Folder, &p.Name, &p.Source, &p.Revision, &manifest, &hashes, &p.InstalledAt); err != nil {
		return InstalledPackage{}, err
	}
	if err := json.Unmarshal([]byte(manifest), &p.Manifest); err != nil {
		return InstalledPackage{}, err
	}
	if err := json.Unmarshal([]byte(hashes), &p.AssetHashes); err != nil {
		return InstalledPackage{}, err
	}
	return p, nil
}
