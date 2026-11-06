package routd

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestOperatorSeededWithGrantOption(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := store.NewOver(db.Obj()).ListACL("role:operator")
	if err != nil || len(rows) != 0 {
		t.Fatalf("stored operator rows = %v, %v", rows, err)
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
