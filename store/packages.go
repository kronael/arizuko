package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// InstalledPackage is one installed_packages record. Manifest maps an asset
// kind to the identities this install owns; AssetHashes maps an asset id to
// the content hash written, so upgrade detects a dirty asset. Folder is the
// half of the key composition needs. An empty folder means the whole instance.
type InstalledPackage struct {
	Folder      string
	Name        string
	Source      string
	Revision    string
	Manifest    map[string][]string
	AssetHashes map[string]string
	InstalledAt string
}

// PutInstalledPackage upserts a record. An unconditional Put: the coordinate
// is (folder, name) and every other field is the install's own answer.
// Manifest and AssetHashes are DOCUMENTS — JSON text, nil normalised to `{}`,
// the encoding the resource's `db:` tags declare (resreg/codec.go); a null
// where a document belongs is a record `remove` cannot reverse.
func (s *Store) PutInstalledPackage(p InstalledPackage) error {
	key, err := InstalledPackageKey(p.Folder, p.Name)
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
		AttrPkgManifest:    manifest,
		AttrPkgAssetHashes: hashes,
	}
	setPkgAttr(attrs, AttrPkgSource, p.Source)
	setPkgAttr(attrs, AttrPkgRevision, p.Revision)
	setPkgAttr(attrs, AttrPkgInstalledAt, p.InstalledAt)
	err = s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: InstalledPackages, Key: key, Attrs: attrs,
	})
	return err
}

func setPkgAttr(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

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
// An empty folder reaches a package installed instance-wide.
func (s *Store) InstalledPackage(folder, name string) (InstalledPackage, bool, error) {
	key, err := InstalledPackageKey(folder, name)
	if err != nil {
		return InstalledPackage{}, false, err
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: InstalledPackages, Partition: OwnerRoutd,
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
func (s *Store) InstalledPackages() ([]InstalledPackage, error) {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: InstalledPackages, Partition: OwnerRoutd,
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
func (s *Store) DeleteInstalledPackage(folder, name string) (bool, error) {
	key, err := InstalledPackageKey(folder, name)
	if err != nil {
		return false, err
	}
	return s.TryWriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Delete, Entity: InstalledPackages, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
}

func installedPackageFrom(it objstore.Item) (InstalledPackage, error) {
	folder, name, err := InstalledPackageCoord(it)
	if err != nil {
		return InstalledPackage{}, err
	}
	p := InstalledPackage{
		Folder:      folder,
		Name:        name,
		Source:      it.Attrs[AttrPkgSource],
		Revision:    it.Attrs[AttrPkgRevision],
		InstalledAt: it.Attrs[AttrPkgInstalledAt],
	}
	if err := readDoc(it.Attrs[AttrPkgManifest], &p.Manifest); err != nil {
		return InstalledPackage{}, fmt.Errorf("installed package %s/%s manifest: %w", folder, name, err)
	}
	if err := readDoc(it.Attrs[AttrPkgAssetHashes], &p.AssetHashes); err != nil {
		return InstalledPackage{}, fmt.Errorf("installed package %s/%s asset hashes: %w", folder, name, err)
	}
	return p, nil
}

func readDoc(raw string, into any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}
