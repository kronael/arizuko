package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestWorldPolicyRows(t *testing.T) {
	db := testDB(t)
	if err := writeWorld(db, "world", "alice", "user:alice", ""); err != nil {
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

func TestWorldWriteRejectsMovedScope(t *testing.T) {
	db := testDB(t)
	if err := db.AddMembership("user:op", store.RoleOperator, "test"); err != nil {
		t.Fatal(err)
	}
	version, err := checkIssuer(db, "user:op", store.WorldGrantRows("user:bob", "world"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutACLRow(core.ACLRow{Principal: "user:other", Action: "admin", Scope: "other", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if err := writeWorld(db, "world", "bob", "user:bob", version); !errors.Is(err, objstore.ErrScopeMoved) {
		t.Fatalf("write with stale version: %v", err)
	}
	if _, ok := db.GroupByFolder("world"); ok {
		t.Fatal("world written with stale proof")
	}
}
