package store

// installed_packages.go declares `installed_packages`: one item per install
// record, addressed by (folder, name). This package declares the shape and
// routd writes it (`routd/packages_store.go`): a second reader, never a second
// format. The group delete reads the folder out of the coordinate
// (folder_scope.go).
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd). The folder leads the
// coordinate so a group's installs are a subtree (BUGS F30); folder "" is
// instance-wide and a legal coordinate.
//
// `manifest` and `asset_hashes` are Go maps written as JSON text, the encoding
// the resource's `db:` tags declare (resreg/codec.go). A nil map stores `{}`
// and never `null`: a null where a document belongs is a record `remove`
// cannot reverse.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// The attributes an install record carries. `folder` and `name` are the
// coordinate. Exported because the writer is in another package.
const (
	AttrPkgSource      = "source"
	AttrPkgRevision    = "revision"
	AttrPkgInstalledAt = "installed_at"
	AttrPkgManifest    = "manifest"
	AttrPkgAssetHashes = "asset_hashes"
)

// InstalledPackageKey is the coordinate of one install record. A value carrying
// the separator is REFUSED: two records would address one item, and `remove`
// would reverse identities the other one owns.
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
// that does not split is a LOUD fault: it names an install nobody ran.
func InstalledPackageCoord(it objstore.Item) (folder, name string, err error) {
	folder, name, ok := strings.Cut(it.Key.Sort, KeySep)
	if !ok {
		return "", "", fmt.Errorf("installed package %q does not split into (folder, name)",
			it.Key.Sort)
	}
	return folder, name, nil
}
