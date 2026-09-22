// Package dynamo_test runs the real store over a DynamoDB endpoint, so the port
// is proved by the code daemons call rather than by the driver's own suite.
package dynamo_test

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// routdStore hands back a handle whose routd entities, version marker and audit
// record are already made — what every opener of routd's store needs.
func routdHandle(t *testing.T) *dynstore.Store {
	t.Helper()
	d := dyntest.Store(t)
	ctx := context.Background()
	if err := store.PrepareOwner(ctx, d, store.OwnerRoutd); err != nil {
		t.Fatalf("make routd's entities: %v", err)
	}
	// The version marker is one item per subsystem and lives beside them.
	if err := d.CreateEntity(ctx, store.ScopeEntity); err != nil {
		t.Fatalf("make the scope entity: %v", err)
	}
	// Every audited write carries a record, and audit owns that declaration.
	if err := audit.EnsureEntities(ctx, d); err != nil {
		t.Fatalf("make the audit entities: %v", err)
	}
	return d
}

// openStore builds the real store over the endpoint, with routd's entities made.
func openStore(t *testing.T) *store.Store {
	return store.NewOver(routdHandle(t))
}

// A group, a route and a grant go in and come back out, through the same
// methods a daemon calls. Nothing here names SQL.
func TestStoreRunsOverDynamo(t *testing.T) {
	s := openStore(t)

	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	folders, err := s.GroupFolders(0)
	if err != nil {
		t.Fatalf("GroupFolders: %v", err)
	}
	if len(folders) != 1 || folders[0] != "acme" {
		t.Fatalf("GroupFolders = %v, want [acme]", folders)
	}

	id, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "acme"})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if id == 0 {
		t.Fatal("AddRoute answered id 0")
	}
	again, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "acme"})
	if err != nil {
		t.Fatalf("AddRoute(duplicate): %v", err)
	}
	if again != id {
		t.Errorf("the duplicate answered %d, want the rule's own id %d", again, id)
	}

	if err := s.AddACLRow(core.ACLRow{
		Principal: "user:u1", Action: "admin", Scope: "acme", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	rows, err := s.ACLRowsFor([]string{"user:u1"})
	if err != nil {
		t.Fatalf("ACLRowsFor: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Principal == "user:u1" && r.Scope == "acme" {
			found = true
		}
	}
	if !found {
		t.Errorf("the grant did not read back; %d rows", len(rows))
	}
}

// routd itself, served from the endpoint. The daemon's own door is what a live
// instance opens, so this is the wiring rather than the store under it.
func TestRoutdRunsOverDynamo(t *testing.T) {
	rdb := routd.OpenOver(routdHandle(t))

	if err := rdb.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup through routd: %v", err)
	}
	if _, err := rdb.AddRoute(core.Route{Match: "chat_jid=telegram:9", Target: "acme"}); err != nil {
		t.Fatalf("AddRoute through routd: %v", err)
	}
	routes, err := rdb.Routes()
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if len(routes) != 1 || routes[0].Target != "acme" {
		t.Fatalf("Routes = %+v, want one rule targeting acme", routes)
	}
	groups := rdb.AllGroups()
	if _, ok := groups["acme"]; !ok || len(groups) != 1 {
		t.Fatalf("AllGroups = %v, want just acme", groups)
	}
}

// authd and runed take the handle directly, so this is what "wired" means for
// them: their own stores, over the endpoint, doing their own work.
func TestAuthdAndRunedRunOverDynamo(t *testing.T) {
	d := dyntest.Store(t)
	ctx := context.Background()
	for _, e := range append(authstore.Entities, runstore.Entities...) {
		if err := d.CreateEntity(ctx, e); err != nil {
			t.Fatalf("make %s: %v", e, err)
		}
	}

	as := authstore.New(d)
	if err := as.LinkOAuthIdentity("u1", "Alice", "google", "g-1"); err != nil {
		t.Fatalf("authd links an identity: %v", err)
	}
	owner, name, _, _, ok := as.OAuthIdentityForSub("google:g-1")
	if !ok || owner != "u1" || name != "Alice" {
		t.Fatalf("the identity read back as %q/%q (found=%v)", owner, name, ok)
	}

	rs := runstore.New(d)
	if err := rs.CreateSpawn(ctx, runstore.Spawn{RunID: "run_1", Folder: "acme"}); err != nil {
		t.Fatalf("runed records a spawn: %v", err)
	}
	sp, err := rs.SpawnByRunID(ctx, "run_1")
	if err != nil {
		t.Fatalf("runed reads its spawn: %v", err)
	}
	if sp.Folder != "acme" {
		t.Errorf("the spawn read back on folder %q, want acme", sp.Folder)
	}
}
