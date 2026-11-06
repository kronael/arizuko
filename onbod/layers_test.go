package main

import (
	"slices"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestWorldPolicyRows(t *testing.T) {
	db := testDB(t)
	if err := writeWorld(db, "world", "alice", "user:alice"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListACL("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("world grants = %+v", rows)
	}
	for _, want := range []core.ACLRow{
		{Principal: "user:alice", Action: "admin", Scope: "world/**", Effect: store.EffectAllow},
		{Principal: "folder:world", Action: "mcp:*", Scope: "world/**", Effect: store.EffectAllow},
	} {
		if !slices.ContainsFunc(rows, func(row core.ACLRow) bool {
			return row.Principal == want.Principal && row.Action == want.Action && row.Scope == want.Scope && row.Effect == want.Effect && !row.GrantOption
		}) {
			t.Errorf("missing world grant %+v: %+v", want, rows)
		}
	}
	parents, err := db.Ancestors("folder:world")
	if err != nil || !slices.Contains(parents, store.RoleWorld) || !slices.Contains(parents, store.RoleMember) {
		t.Fatalf("world roles = %v, %v", parents, err)
	}
	if err := db.AddMembership("user:operator", store.RoleOperator, "test"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(userFolders(db, "user:operator"), "**") {
		t.Fatal("operator scope missing")
	}
}
