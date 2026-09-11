package routd

// packages_store.go is the installed-package record (spec 5/28): CRUD over
// `installed_packages`. It generalises 5/20's products.lock — one item per
// installed package holding the source + resolved revision, the manifest of
// identities the install owns, and a per-asset content hash. The `packages
// install|remove` machinery reads/writes it; it is the basis for upgrade
// (diff new vs recorded manifest), remove (delete recorded identities), and
// dirty-detection (current hash vs recorded hash). NOT per-row provenance.
//
// THE COORDINATE AND THE ATTRIBUTE NAMES ARE store's (store/installed_packages.go)
// AND THE WRITES ARE routd's. That file declares the shape because a group delete
// reads the folder out of the coordinate; this file writes it because the install
// machinery lives here. One declaration, two readers — never two spellings, which
// would put an install where the purge does not look.
//
// The writes go through *store.Store rather than the driver, because a write that
// reached objstore itself would move no version marker and be invisible to every
// `arizuko export` of routd's projection (BUGS L264).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// InstalledPackage is one installed_packages record. Manifest maps an asset kind
// (skills / compose_fragment / proxyd_route / acl / group_seed) to the
// identities this install owns; AssetHashes maps an asset id to the content
// hash written, so upgrade can detect a locally-edited (dirty) asset.
//
// Folder is the half of the key spec 5/28's composition needs: a group blends an
// ordered product mix, so the lock has to name WHICH group. InstanceWide ("") is
// the sentinel for a package installed against the whole instance — every record
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

// InstanceWide is installed_packages' folder for a package that belongs to the
// instance rather than to one group. `arizuko packages install` writes compose
// fragments, proxyd routes and host files — none of which belong to a group — so
// it is the only value the CLI produces.
const InstanceWide = ""

// PutInstalledPackage upserts a package's record (install and upgrade).
//
// It is an unconditional Put: the coordinate is (folder, name) and every other
// field is the install's own answer, so a re-install replaces the whole record
// rather than merging into the one it supersedes.
//
// Manifest and AssetHashes are DOCUMENTS — their JSON text, with a nil map
// normalised to the empty one, which is the encoding the resource declares in
// its `db:` tags (resreg/codec.go) and therefore what resreg's own writer of
// these items renders. A null where a document belongs is a record `remove`
// cannot reverse.
func (d *DB) PutInstalledPackage(p InstalledPackage) error {
	key, err := store.InstalledPackageKey(p.Folder, p.Name)
	if err != nil {
		return err
	}
	manifest, err := jsonDoc(p.Manifest)
	if err != nil {
		return fmt.Errorf("installed package %s manifest: %w", p.Name, err)
	}
	hashes, err := jsonDoc(p.AssetHashes)
	if err != nil {
		return fmt.Errorf("installed package %s asset hashes: %w", p.Name, err)
	}
	attrs := map[string]string{
		store.AttrPkgManifest:    manifest,
		store.AttrPkgAssetHashes: hashes,
	}
	setPkgAttr(attrs, store.AttrPkgSource, p.Source)
	setPkgAttr(attrs, store.AttrPkgRevision, p.Revision)
	setPkgAttr(attrs, store.AttrPkgInstalledAt, p.InstalledAt)
	err = d.store().WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: store.InstalledPackages, Key: key, Attrs: attrs,
	})
	return err
}

// setPkgAttr records one optional attribute, leaving it off the item when the
// install does not carry it. Absent is the only null this model has, so an empty
// string must never be written where a reader tests for the field's presence —
// the same rule resreg's own writer of these items follows (resreg/entity.go
// itemFor).
func setPkgAttr(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// jsonDoc renders a map as the JSON text the item stores, with nil normalised to
// the empty document so `{}` lands and never `null`.
func jsonDoc(v any) (string, error) {
	switch m := v.(type) {
	case map[string][]string:
		if m == nil {
			v = map[string][]string{}
		}
	case map[string]string:
		if m == nil {
			v = map[string]string{}
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// InstalledPackage returns one record by its full key; ok=false when absent.
// Pass InstanceWide for folder to reach a package installed instance-wide.
func (d *DB) InstalledPackage(folder, name string) (InstalledPackage, bool, error) {
	key, err := store.InstalledPackageKey(folder, name)
	if err != nil {
		return InstalledPackage{}, false, err
	}
	items, err := d.Obj().Query(context.Background(), objstore.Query{
		Entity: store.InstalledPackages, Partition: store.OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return InstalledPackage{}, false, err
	}
	if len(items) == 0 {
		return InstalledPackage{}, false, nil
	}
	p, err := installedPackageFrom(items[0])
	if err != nil {
		return InstalledPackage{}, false, err
	}
	return p, true, nil
}

// InstalledPackages lists every record, instance-wide rows first (folder ""
// sorts before any group), then by name — the coordinate's own order.
func (d *DB) InstalledPackages() ([]InstalledPackage, error) {
	items, err := d.Obj().Query(context.Background(), objstore.Query{
		Entity: store.InstalledPackages, Partition: store.OwnerRoutd,
	})
	if err != nil {
		return nil, err
	}
	out := make([]InstalledPackage, 0, len(items))
	for _, it := range items {
		p, err := installedPackageFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// DeleteInstalledPackage removes a record (remove). ok=false when absent — the
// delete conditions on the item being there, so the refusal IS the not-found
// answer and `remove` never reports having reversed an install nobody made.
func (d *DB) DeleteInstalledPackage(folder, name string) (bool, error) {
	key, err := store.InstalledPackageKey(folder, name)
	if err != nil {
		return false, err
	}
	return d.store().TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Delete, Entity: store.InstalledPackages, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

// installedPackageFrom reads one item back. A key that does not split into
// (folder, name) is a LOUD fault rather than half a record: it names an install
// nobody ran, and `remove` reading it would reverse identities another package
// owns.
func installedPackageFrom(it objstore.Item) (InstalledPackage, error) {
	folder, name, err := store.InstalledPackageCoord(it)
	if err != nil {
		return InstalledPackage{}, err
	}
	p := InstalledPackage{
		Folder:      folder,
		Name:        name,
		Source:      it.Attrs[store.AttrPkgSource],
		Revision:    it.Attrs[store.AttrPkgRevision],
		InstalledAt: it.Attrs[store.AttrPkgInstalledAt],
	}
	if err := readDoc(it.Attrs[store.AttrPkgManifest], &p.Manifest); err != nil {
		return InstalledPackage{}, fmt.Errorf("installed package %s/%s manifest: %w", folder, name, err)
	}
	if err := readDoc(it.Attrs[store.AttrPkgAssetHashes], &p.AssetHashes); err != nil {
		return InstalledPackage{}, fmt.Errorf("installed package %s/%s asset hashes: %w", folder, name, err)
	}
	return p, nil
}

// readDoc parses a document attribute. Absent is the empty document, which is
// what the column's `{}` default held; anything else that is not the document it
// claims to be is a fault, because a record read wrong is one `remove` acts on.
func readDoc(raw string, into any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}
