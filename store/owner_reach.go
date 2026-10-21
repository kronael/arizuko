package store

// owner_reach.go declares which owner's tables each daemon opens. Every daemon
// signs into the store server as its own IAM user, and the policy on that user
// is written from this map; nothing checked those policies before, and all 19
// on sloth carried `dynamodb:*` on `*` (BUGS L318).
//
// TestStoreReachMatchesTheCallSites fails when a daemon opens an owner this map
// does not list, or lists one it does not open: a policy written from prose
// drifted once, and one that is too narrow fails only when the daemon first
// reaches the table.
//
// An ADAPTER's prefix is its own service name (chanlib.OpenAdapterStore); gitd
// and slakd also read routd's tables directly. An EMPTY list opens no store and
// needs no key: timed writes through the owner's HTTP API instead.
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
	"emaid":  {"emaid"},
	"timed":  {},
	"ttsd":   {},
	// The operator CLI: routd for groups, grants, routes and secrets; onbod for
	// invites and gates; authd for the signing keys `token issue bearer` reads.
	"cmd/arizuko": {OwnerAuthd, OwnerOnbod, OwnerRoutd},
}
