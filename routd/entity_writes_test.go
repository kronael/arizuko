package routd

// entity_writes_test.go covers the three of routd's thirteen entities whose
// coordinate and attribute names store DECLARES and routd WRITES —
// `installed_packages`, `web_routes` and the per-group cap on `groups`. store
// has no test over them because store has no writer for them, so this is the
// only place the round trip is asserted.

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// A cap write must not take the settings document with it: the cap and the
// settings are two attributes of one item, and a Put would carry the whole item
// — so the write is an Update.
//
// The cap is always WRITTEN, "0" included, because resreg's own writer of these
// items renders every int field always and the two writers have to agree byte
// for byte. The item read below is what makes that comparable; reading the cap
// back through FolderCap alone cannot tell "0" from an absent attribute, because
// costCapOf answers 0 for both.
func TestFolderCap_ClearedCapLeavesTheSettingsAlone(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "acme", Model: "claude-opus"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFolderCap("acme", 250); err != nil {
		t.Fatal(err)
	}
	if got, err := db.FolderCap("acme"); err != nil || got != 250 {
		t.Fatalf("FolderCap = (%d, %v), want 250", got, err)
	}
	if err := db.SetFolderCap("acme", 0); err != nil {
		t.Fatal(err)
	}
	if got, err := db.FolderCap("acme"); err != nil || got != 0 {
		t.Fatalf("FolderCap after the clear = (%d, %v), want uncapped", got, err)
	}
	if m, _ := db.GroupConfig("acme"); m != "claude-opus" {
		t.Errorf("model = %q after two cap writes, want claude-opus: the cap and the "+
			"settings are two attributes of one item and neither write owns the other", m)
	}
	items, err := db.Obj().Query(context.Background(), objstore.Query{
		Entity: store.Groups, Partition: store.OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: "acme"},
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("read the group item = (%d items, %v)", len(items), err)
	}
	if got := items[0].Attrs["cost_cap_cents_per_day"]; got != "0" {
		t.Errorf("the cleared cap is stored as %q, want \"0\": resreg's writer of this "+
			"item renders every int field always, and two writers of one item that "+
			"disagree make a diff report a change neither made", got)
	}
}

// A web route's first claim records `created_at`, and re-pointing the rule must
// not restamp it — the upsert is a create-if-absent whose REFUSAL selects the
// merge, so the three fields the upsert owns move and nothing else does.
// Clearing `redirect_to` takes the attribute away: absent is the only null this
// model has, and an Update that left it alone would keep the target of a rule
// the operator has since pointed elsewhere.
func TestWebRoute_UpsertKeepsTheFirstClaimsInstant(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	putWebRoute(t, db, WebRouteRow{
		PathPrefix: "/p/", Access: "redirect", Folder: "acme", RedirectTo: "/pub/acme/x",
	})
	first, err := db.WebRoutes("acme")
	if err != nil || len(first) != 1 {
		t.Fatalf("WebRoutes = (%v, %v)", first, err)
	}
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/p/", Access: "public", Folder: "acme"})
	second, err := db.WebRoutes("acme")
	if err != nil || len(second) != 1 {
		t.Fatalf("WebRoutes after the upsert = (%v, %v)", second, err)
	}
	if second[0].CreatedAt != first[0].CreatedAt {
		t.Errorf("created_at %q -> %q across an upsert: the rule was claimed once",
			first[0].CreatedAt, second[0].CreatedAt)
	}
	if second[0].Access != "public" || second[0].RedirectTo != "" {
		t.Errorf("the upsert left %+v, want access public and no redirect target",
			second[0])
	}
}

// The delete's folder term IS the containment: a caller resolving another
// folder names an item it may not remove, and the answer is the same "no such
// route" an absent one gets — never somebody else's rule.
func TestWebRoute_CrossFolderDeleteRemovesNothing(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/p/", Access: "public", Folder: "acme"})
	notMine := errors.New("no such route of this folder")
	ws := &store.WriteSet{}
	if err := db.deleteWebRouteInto(ws, "/p/", "rival", notMine); err != nil {
		t.Fatal(err)
	}
	if err := db.store().Apply(context.Background(), ws); !errors.Is(err, notMine) {
		t.Errorf("a cross-folder delete answered %v, want the not-found answer", err)
	}
}

// An install record round-trips its two DOCUMENT attributes, and `remove`
// answers false the second time: a delete that reported success on an install
// nobody made would claim to have reversed identities another package owns.
func TestInstalledPackage_RoundTripAndSingleRemoval(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutInstalledPackage(InstalledPackage{
		Folder: InstanceWide, Name: "ttsd", Source: "gh", Revision: "a1",
		Manifest:    map[string][]string{"skills": {"x"}},
		InstalledAt: "2026-08-01T10:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.InstalledPackage(InstanceWide, "ttsd")
	if err != nil || !ok {
		t.Fatalf("read = (%v, %v)", ok, err)
	}
	if got.Source != "gh" || got.Revision != "a1" ||
		len(got.Manifest["skills"]) != 1 || got.Manifest["skills"][0] != "x" {
		t.Fatalf("round trip = %+v", got)
	}
	// A nil map stores `{}` and reads back empty, never null: the record states
	// what an install DID, and a null where a document belongs is a record
	// `remove` cannot reverse.
	if got.AssetHashes == nil || len(got.AssetHashes) != 0 {
		t.Errorf("asset_hashes = %v, want the empty document", got.AssetHashes)
	}
	if all, err := db.InstalledPackages(); err != nil || len(all) != 1 {
		t.Fatalf("list = (%v, %v)", all, err)
	}
	if gone, err := db.DeleteInstalledPackage(InstanceWide, "ttsd"); err != nil || !gone {
		t.Fatalf("remove = (%v, %v), want (true, nil)", gone, err)
	}
	if gone, err := db.DeleteInstalledPackage(InstanceWide, "ttsd"); err != nil || gone {
		t.Fatalf("a second remove = (%v, %v), want (false, nil)", gone, err)
	}
}
