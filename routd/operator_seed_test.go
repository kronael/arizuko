package routd

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// F1 (adversary audit): a fresh routd.db must seed role:operator WITH GRANT OPTION,
// else auth.Delegate's root can delegate nothing once wired (spec 4/R step 3).
func TestOperatorSeededWithGrantOption(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var got int
	if err := db.SQL().QueryRow(
		`SELECT grant_option FROM acl WHERE principal='role:operator' AND action='*'`,
	).Scan(&got); err != nil {
		t.Fatalf("role:operator (*, **) row missing from fresh routd.db: %v", err)
	}
	if got != 1 {
		t.Fatalf("role:operator grant_option = %d, want 1", got)
	}

	// End-to-end: the operator (via membership) can delegate anything.
	st := store.New(db.SQL())
	if err := st.AddMembership("google:op", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	if err := auth.Delegate(st, "google:op", []core.ACLRow{
		{Principal: "folder:acme/eng", Action: "mcp:send", Scope: "acme/eng"},
	}); err != nil {
		t.Fatalf("operator should delegate anything, got: %v", err)
	}
}
