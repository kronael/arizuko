package store

// owner_reach.go declares which owner's tables each daemon opens.
//
// IT EXISTS BECAUSE A STORE KEY IS SCOPED BY HAND. Every daemon signs into the
// instance's store server as its own IAM user with its own access key, and the
// policy on that user is what bounds it to the tables it actually reaches.
// Nothing generates those policies and nothing checked them, so all 19 on sloth
// carried `{"Action":"dynamodb:*","Resource":"*"}` — attribution without
// isolation, an adapter compromise reaching every owner's tables (BUGS L318).
//
// The prefix is the boundary: dynstore.Prefix puts the owner in front of every
// entity name, so `routd.acl` is routd's and no other daemon's, and a policy
// resource of `routd.*` is exactly "routd's tables".
//
// THE MAP IS THE DECLARATION AND THE CALL SITES ARE THE TRUTH.
// TestStoreReachMatchesTheCallSites walks the module for the doors below and
// fails when a daemon opens an owner this map does not list, or lists one it
// does not open. A policy written from prose drifted once already — INSTALL.md
// gave dashd `runed.*`, which it has never opened — and a policy that is too
// narrow does not fail at deploy, it fails the first time the daemon reaches
// the table it was never granted.
//
// An ADAPTER's prefix is its own service name (chanlib.OpenAdapterStore), not
// one of the four owners, because its tables are its own. Two adapters need
// more than that: gitd and slakd read routd's tables directly, so their policy
// carries `routd.*` as well and an adapter-prefix-only policy breaks them.
//
// A daemon with an EMPTY list opens no store at all and needs no key. timed is
// the split write-discipline working: it writes through the owner's HTTP API
// with a service token instead.
var StoreReach = map[string][]string{
	"authd":  {OwnerAuthd},
	"routd":  {OwnerRoutd},
	"runed":  {OwnerRuned},
	"onbod":  {OwnerOnbod, OwnerRoutd},
	"dashd":  {OwnerOnbod, OwnerRoutd},
	"webd":   {OwnerRoutd},
	"proxyd": {OwnerRoutd},
	"gitd":   {OwnerRoutd, "gitd"},
	"slakd":  {OwnerRoutd, "slakd"},
	"teled":  {"teled"},
	"discd":  {"discd"},
	"bskyd":  {"bskyd"},
	"linkd":  {"linkd"},
	"emaid":  {"emaid"},
	"reditd": {"reditd"},
	"timed":  {},
	"ttsd":   {},
	// The operator CLI runs on the host and reaches three owners: routd for
	// groups, grants, routes and secrets, onbod for invites and gates, and authd
	// for the signing keys `token issue bearer` reads.
	"cmd/arizuko": {OwnerAuthd, OwnerOnbod, OwnerRoutd},
}
