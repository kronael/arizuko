package routd

// packages_store.go is the installed-package record (EXTENDING.md § Installing packages): one item per
// installed package with its source + revision, the identities the install
// owns, and a per-asset content hash — the basis for upgrade, remove and
// dirty-detection. NOT per-row provenance. The coordinate and attribute names
// are store's (a group delete reads the folder off the coordinate); the
// writes are routd's, through *store.Store, never the driver (BUGS L264).

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// InstalledPackage is one installed_packages record. Manifest maps an asset
// kind to the identities this install owns; AssetHashes maps an asset id to
// the content hash written, so upgrade detects a dirty asset. Folder is the
// half of the key composition needs (a group blends an ordered product mix);
// InstanceWide ("") is a package installed against the whole instance.
type InstalledPackage struct {
	Folder      string
	Name        string
	Source      string
	Revision    string
	Manifest    map[string][]string
	AssetHashes map[string]string
	InstalledAt string
}

// InstanceWide is the folder of a package that belongs to the instance rather
// than to one group — the only value the CLI produces.
const InstanceWide = ""

// PutInstalledPackage upserts a record. An unconditional Put: the coordinate
// is (folder, name) and every other field is the install's own answer.
// Manifest and AssetHashes are DOCUMENTS — JSON text, nil normalised to `{}`,
// the encoding the resource's `db:` tags declare (resreg/codec.go); a null
// where a document belongs is a record `remove` cannot reverse.
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

// setPkgAttr leaves an empty value OFF the item: absent is the only null, the
// same rule resreg's own writer follows (resreg/entity.go itemFor).
func setPkgAttr(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// jsonDoc renders a map as the JSON text the item stores, nil normalised to
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

// DeleteInstalledPackage removes a record; ok=false when absent. The delete
// conditions on the item being there, so `remove` never reports having
// reversed an install nobody made.
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

// SelectionOrder is the precedence of selected packages, lowest first: core,
// then the others in the order they were selected, whatever their scope. A
// later package overrides an earlier one on the same installed path.
func SelectionOrder(a, b InstalledPackage) int {
	if aCore, bCore := isCorePackage(a), isCorePackage(b); aCore != bCore {
		if aCore {
			return -1
		}
		return 1
	}
	return cmp.Or(cmp.Compare(a.InstalledAt, b.InstalledAt), cmp.Compare(a.Folder, b.Folder), cmp.Compare(a.Name, b.Name))
}

func isCorePackage(p InstalledPackage) bool {
	return p.Folder == InstanceWide && p.Name == container.CorePackageName
}

// SelectedPackages returns the packages a turn in folder mounts — core, which
// every turn has, then the synced selections in SelectionOrder — and the group
// files that package sync wrote into folder.
func (d *DB) SelectedPackages(folder string) (names, managed []string, err error) {
	records, err := d.InstalledPackages()
	slices.SortStableFunc(records, SelectionOrder)
	names = []string{container.CorePackageName}
	for _, record := range records {
		if _, assets := record.Manifest["skill"]; assets && !isCorePackage(record) && (record.Folder == InstanceWide || record.Folder == folder) {
			names = append(names, record.Name)
		}
		if record.Folder == folder {
			managed = append(managed, record.Manifest[container.ManifestGroupFiles]...)
		}
	}
	return names, slices.Compact(slices.Sorted(slices.Values(managed))), err
}

// installedPackageFrom reads one item back. A key that does not split into
// (folder, name) is a LOUD fault, not half a record: it names an install
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

// readDoc parses a document attribute; absent is the empty document. Anything
// else that is not the document it claims to be is a fault, because a record
// read wrong is one `remove` acts on.
func readDoc(raw string, into any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}
