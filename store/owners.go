package store

// Owner names — the daemons that own and migrate a SQLite file under store/.
// `messages.db` is deliberately absent: the frozen pre-split monolith has no
// owner and no daemon opens it.
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
