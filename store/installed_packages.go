package store

// installed_packages.go declares `installed_packages`: one item per install
// record, addressed by (folder, name).
//
// THIS PACKAGE DECLARES THE SHAPE AND routd WRITES IT
// (`routd/packages_store.go`), for the reason store/pending_actions.go states:
// two consumers need the same facts in different shapes, which justifies a
// second reader and never a second format. This package's group delete reads the
// folder out of the coordinate (folder_scope.go).
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// `installed_packages` is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd).
//
// THE FOLDER LEADS THE COORDINATE, and that is what makes a group's installs
// readable as a subtree: composition blends a product mix per GROUP, so the
// record names which group (BUGS F30). Folder "" is instance-wide — every row
// today — and it is a legal coordinate: the separator still divides the two
// parts, so the read splits it back the same way.
//
// TWO ATTRIBUTES ARE DOCUMENTS. `manifest` and `asset_hashes` are Go maps
// written as their JSON text, which is the encoding the resource declares in its
// `db:` tags (resreg/codec.go). A nil map is normalised to the empty one before
// it renders, so it stores `{}` and never `null`: the record states what an
// install DID, and a null where a document belongs is a record `remove` cannot
// reverse.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// The attributes an install record carries. `folder` and `name` are NOT among
// them: they are the coordinate. Exported because the writer is in another
// package.
const (
	AttrPkgSource      = "source"
	AttrPkgRevision    = "revision"
	AttrPkgInstalledAt = "installed_at"
	AttrPkgManifest    = "manifest"
	AttrPkgAssetHashes = "asset_hashes"
)

// InstalledPackageKey is the coordinate of one install record.
//
// A value carrying the separator is REFUSED rather than joined: two different
// records would address one item, the last install would win, and `remove` would
// then reverse identities the other one owns.
func InstalledPackageKey(folder, name string) (objstore.Key, error) {
	if name == "" {
		return objstore.Key{}, fmt.Errorf("installed package in %q carries no name, "+
			"which is half its coordinate", folder)
	}
	for label, v := range map[string]string{"folder": folder, "name": name} {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("installed package %s=%q carries the byte that "+
				"separates the two fields of its coordinate", label, v)
		}
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: folder + KeySep + name}, nil
}

// InstalledPackageCoord splits one item's key back into (folder, name). A key
// that does not split is a LOUD fault rather than half a record: it names an
// install nobody ran.
func InstalledPackageCoord(it objstore.Item) (folder, name string, err error) {
	folder, name, ok := strings.Cut(it.Key.Sort, KeySep)
	if !ok {
		return "", "", fmt.Errorf("installed package %q does not split into (folder, name)",
			it.Key.Sort)
	}
	return folder, name, nil
}
