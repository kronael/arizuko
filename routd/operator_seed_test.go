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
	if err := authDelegateForTest(st, "google:op", []core.ACLRow{
		{Principal: "folder:acme/eng", Action: "mcp:send", Scope: "acme/eng"},
	}); err != nil {
		t.Fatalf("operator should delegate anything, got: %v", err)
	}
}

func authDelegateForTest(s *store.Store, granter string, rows []core.ACLRow) error {
	mutations := make([]auth.Mutation, len(rows))
	for i, row := range rows {
		mutations[i] = auth.Mutation{Row: row}
	}
	_, err := auth.CheckGrant(s, auth.Caller{Principal: granter}, auth.Ask{}, mutations...)
	return err
}
