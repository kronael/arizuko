package store

// Owner names — the daemons that own a set of tables on the instance's store
// server. The name is what `dynstore.Prefix` puts in front of every entity, so
// `routd.acl` is routd's and no other daemon's.
const (
	OwnerAuthd = "authd"
	OwnerOnbod = "onbod"
	OwnerRoutd = "routd"
	OwnerRuned = "runed"
)

// owners is the registry every door validates against. It is a SET rather than
// a list because the only question asked of it is membership.
var owners = map[string]bool{
	OwnerAuthd: true,
	OwnerOnbod: true,
	OwnerRoutd: true,
	OwnerRuned: true,
}

// mustOwner panics on an unregistered owner: every caller passes one of the
// constants above, so a miss is a programming error, and answering anyway would
// silently point a daemon at a prefix nobody serves.
func mustOwner(owner string) string {
	if !owners[owner] {
		panic("store: unknown table owner " + owner)
	}
	return owner
}
