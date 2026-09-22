package routd

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// F1 (adversary audit): a fresh routd.db must seed role:operator WITH GRANT OPTION,
// else auth.Delegate's root can delegate nothing once wired (spec 5/33-paths-roles step 3).
func TestOperatorSeededWithGrantOption(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := store.NewOver(db.Obj()).ListACL("role:operator")
	if err != nil {
		t.Fatal(err)
	}
	seeded := false
	for _, r := range rows {
		if r.Action != "*" {
			continue
		}
		seeded = true
		if !r.GrantOption {
			t.Fatalf("role:operator (*, %s) carries no grant option", r.Scope)
		}
	}
	if !seeded {
		t.Fatal("role:operator (*, **) row missing from a fresh routd.db")
	}

	// End-to-end: the operator (via membership) can delegate anything.
	st := store.NewOver(db.Obj())
	if err := st.AddMembership("google:op", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	if err := auth.Delegate(st, "google:op", []core.ACLRow{
		{Principal: "folder:acme/eng", Action: "mcp:send", Scope: "acme/eng"},
	}); err != nil {
		t.Fatalf("operator should delegate anything, got: %v", err)
	}
}
