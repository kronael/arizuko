package store

import "path/filepath"

// Owner names — the daemons that own and migrate a SQLite file under store/.
// `messages.db` is deliberately absent: the frozen pre-split monolith has no
// owner and no daemon opens it, so it stays flat at store/messages.db.
const (
	OwnerAuthd = "authd"
	OwnerOnbod = "onbod"
	OwnerRoutd = "routd"
	OwnerRuned = "runed"
)

// OwnerDBs maps each owner to its SQLite filename. authd's file is `auth.db`,
// not `authd.db` — the daemon and the database were named separately and that
// file name is on disk in every live instance.
//
// `arizuko create` seeds every entry: owner daemons Open strictly and never
// create their own database, so a wrong path fails loud instead of migrating a
// fresh empty instance that boots green (spec 5/16 step 7).
var OwnerDBs = map[string]string{
	OwnerAuthd: "auth.db",
	OwnerOnbod: "onbod.db",
	OwnerRoutd: "routd.db",
	OwnerRuned: "runed.db",
}

// OwnerDir is the directory under storeDir holding one owner's SQLite file and
// its -wal/-shm siblings. The subdirectory is what makes ownership a boundary
// instead of a convention: compose binds store/<owner> into that owner's
// container, so a flat store/ no longer hands every daemon every DB
// (spec 5/16 step 7). SQLite's WAL sidecars must travel and mount with their
// database — never split a triple across binds.
func OwnerDir(storeDir, owner string) string {
	return filepath.Join(storeDir, mustOwner(owner))
}

// OwnerDBPath is store/<owner>/<file> under storeDir.
func OwnerDBPath(storeDir, owner string) string {
	return filepath.Join(storeDir, mustOwner(owner), OwnerDBs[owner])
}

// mustOwner panics on an unregistered owner: every caller passes one of the
// constants above, so a miss is a programming error, and returning a bare
// storeDir would silently point a daemon at another owner's tree.
func mustOwner(owner string) string {
	if _, ok := OwnerDBs[owner]; !ok {
		panic("store: unknown DB owner " + owner)
	}
	return owner
}
