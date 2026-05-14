package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestACL_CRUD(t *testing.T) {
	s := openMem(t)
	row := core.ACLRow{
		Principal: "google:alice",
		Action:    "admin",
		Scope:     "atlas/**",
		Effect:    "allow",
	}
	if err := s.AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := s.AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	got := s.ACLRowsFor([]string{"google:alice"})
	if len(got) != 1 {
		t.Fatalf("ACLRowsFor: %d rows %+v", len(got), got)
	}
	if got[0].Action != "admin" || got[0].Scope != "atlas/**" {
		t.Fatalf("row contents: %+v", got[0])
	}
	if err := s.RemoveACLRow(row); err != nil {
		t.Fatal(err)
	}
	if got := s.ACLRowsFor([]string{"google:alice"}); len(got) != 0 {
		t.Fatalf("after remove: %v", got)
	}
}

func TestACL_DefaultEffectAllow(t *testing.T) {
	s := openMem(t)
	row := core.ACLRow{
		Principal: "p",
		Action:    "interact",
		Scope:     "f",
	}
	if err := s.AddACLRow(row); err != nil {
		t.Fatal(err)
	}
	got := s.ACLRowsFor([]string{"p"})
	if len(got) != 1 || got[0].Effect != "allow" {
		t.Fatalf("default effect: %+v", got)
	}
}

func TestACL_WildcardRows(t *testing.T) {
	s := openMem(t)
	exact := core.ACLRow{Principal: "google:alice", Action: "admin", Scope: "f"}
	wild := core.ACLRow{Principal: "discord:user/*", Action: "interact", Scope: "f"}
	if err := s.AddACLRow(exact); err != nil {
		t.Fatal(err)
	}
	if err := s.AddACLRow(wild); err != nil {
		t.Fatal(err)
	}
	w := s.ACLWildcardRows()
	if len(w) != 1 || w[0].Principal != "discord:user/*" {
		t.Fatalf("wildcards: %+v", w)
	}
	// Exact-only path should not return wildcards.
	got := s.ACLRowsFor([]string{"google:alice"})
	if len(got) != 1 || got[0].Principal != "google:alice" {
		t.Fatalf("exact-only: %+v", got)
	}
}

func TestACL_DenyCoexistsWithAllow(t *testing.T) {
	s := openMem(t)
	allow := core.ACLRow{Principal: "p", Action: "admin", Scope: "f", Effect: "allow"}
	deny := core.ACLRow{Principal: "p", Action: "admin", Scope: "f", Effect: "deny"}
	if err := s.AddACLRow(allow); err != nil {
		t.Fatal(err)
	}
	if err := s.AddACLRow(deny); err != nil {
		t.Fatal(err)
	}
	got := s.ACLRowsFor([]string{"p"})
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(got), got)
	}
}
