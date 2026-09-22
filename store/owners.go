package store

// Owner names: the daemons that own a set of tables on the instance's store
// server. dynstore.Prefix puts the name in front of every entity, so
// `routd.acl` is routd's and no other daemon's.
const (
	OwnerAuthd = "authd"
	OwnerOnbod = "onbod"
	OwnerRoutd = "routd"
	OwnerRuned = "runed"
)

// owners is the registry every door validates against.
var owners = map[string]bool{
	OwnerAuthd: true,
	OwnerOnbod: true,
	OwnerRoutd: true,
	OwnerRuned: true,
}

// mustOwner panics on an unregistered owner: a miss is a programming error,
// and answering anyway would point a daemon at a prefix nobody serves.
func mustOwner(owner string) string {
	if !owners[owner] {
		panic("store: unknown table owner " + owner)
	}
	return owner
}
