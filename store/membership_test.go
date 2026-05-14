package store

import (
	"errors"
	"testing"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMembership_AddAndIndex(t *testing.T) {
	s := openMem(t)
	if err := s.AddMembership("alice", "role:operator", "admin"); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	if got := s.Members("role:operator"); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("Members: %v", got)
	}
	if got := s.Ancestors("alice"); len(got) != 1 || got[0] != "role:operator" {
		t.Fatalf("Ancestors: %v", got)
	}
	if err := s.RemoveMembership("alice", "role:operator"); err != nil {
		t.Fatal(err)
	}
	if got := s.Members("role:operator"); len(got) != 0 {
		t.Fatalf("after remove: %v", got)
	}
}

func TestMembership_Idempotent(t *testing.T) {
	s := openMem(t)
	if err := s.AddMembership("a", "b", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("a", "b", ""); err != nil {
		t.Fatalf("second AddMembership: %v", err)
	}
	if got := s.Members("b"); len(got) != 1 {
		t.Fatalf("idempotency: %v", got)
	}
}

func TestMembership_SelfRejected(t *testing.T) {
	s := openMem(t)
	err := s.AddMembership("alice", "alice", "")
	if !errors.Is(err, ErrSelfMembership) {
		t.Fatalf("expected ErrSelfMembership, got %v", err)
	}
}

func TestMembership_CycleDirect(t *testing.T) {
	s := openMem(t)
	// a→b
	if err := s.AddMembership("a", "b", ""); err != nil {
		t.Fatal(err)
	}
	// b→a would close cycle.
	err := s.AddMembership("b", "a", "")
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
}

func TestMembership_CycleTransitive(t *testing.T) {
	s := openMem(t)
	// a→b→c
	if err := s.AddMembership("a", "b", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("b", "c", ""); err != nil {
		t.Fatal(err)
	}
	// c→a would close 3-step cycle.
	if err := s.AddMembership("c", "a", ""); !errors.Is(err, ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
}

func TestMembership_AncestorsTransitive(t *testing.T) {
	s := openMem(t)
	if err := s.AddMembership("a", "b", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("b", "c", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembership("c", "d", ""); err != nil {
		t.Fatal(err)
	}
	got := s.Ancestors("a")
	want := map[string]bool{"b": true, "c": true, "d": true}
	if len(got) != 3 {
		t.Fatalf("Ancestors len: %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected ancestor %q in %v", g, got)
		}
	}
}
