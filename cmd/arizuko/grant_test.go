package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

func newMem(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGrantThenList(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, "github:1", "alice", &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}
	if !strings.Contains(out.String(), "granted github:1 -> alice") {
		t.Errorf("grant output = %q", out.String())
	}

	out.Reset()
	if err := runGrants(s, "", &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	if !strings.Contains(out.String(), "github:1") || !strings.Contains(out.String(), "alice") {
		t.Errorf("grants output missing row: %q", out.String())
	}
}

func TestGrantIdempotent(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, "github:1", "**", &out); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	out.Reset()
	if err := runGrant(s, "github:1", "**", &out); err != nil {
		t.Fatalf("second grant should not error: %v", err)
	}
	if !strings.Contains(out.String(), "already granted") {
		t.Errorf("expected 'already granted', got %q", out.String())
	}

	// Still exactly one row.
	gs, err := s.Grants("github:1")
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if len(gs) != 1 {
		t.Fatalf("expected 1 row, got %d", len(gs))
	}
}

func TestUngrantRemoves(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	runGrant(s, "github:1", "alice", &out)
	out.Reset()

	if err := runUngrant(s, "github:1", "alice", &out); err != nil {
		t.Fatalf("runUngrant: %v", err)
	}
	if !strings.Contains(out.String(), "ungranted") {
		t.Errorf("ungrant output = %q", out.String())
	}

	gs, _ := s.Grants("github:1")
	if len(gs) != 0 {
		t.Fatalf("expected 0 rows after ungrant, got %d", len(gs))
	}
}

func TestUngrantNonexistent(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runUngrant(s, "ghost", "nowhere", &out); err != nil {
		t.Fatalf("runUngrant should not error: %v", err)
	}
	if !strings.Contains(out.String(), "no grant to remove") {
		t.Errorf("expected 'no grant to remove', got %q", out.String())
	}
}

func TestGrantsFilterBySub(t *testing.T) {
	s := newMem(t)
	var sink bytes.Buffer

	runGrant(s, "u1", "alpha", &sink)
	runGrant(s, "u1", "beta", &sink)
	runGrant(s, "u2", "gamma", &sink)

	var out bytes.Buffer
	if err := runGrants(s, "u1", &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	s1 := out.String()
	if !strings.Contains(s1, "alpha") || !strings.Contains(s1, "beta") {
		t.Errorf("u1 output missing own rows: %q", s1)
	}
	if strings.Contains(s1, "gamma") {
		t.Errorf("u1 output leaked u2 row: %q", s1)
	}
}

func TestGrantsEmpty(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer
	if err := runGrants(s, "", &out); err != nil {
		t.Fatalf("runGrants: %v", err)
	}
	if !strings.Contains(out.String(), "no grants") {
		t.Errorf("expected 'no grants', got %q", out.String())
	}
}

func TestGrantRejectsEmpty(t *testing.T) {
	s := newMem(t)
	var out bytes.Buffer

	if err := runGrant(s, "", "alice", &out); err == nil {
		t.Error("expected error for empty sub")
	}
	if err := runGrant(s, "u1", "", &out); err == nil {
		t.Error("expected error for empty pattern")
	}
	if err := runUngrant(s, "", "alice", &out); err == nil {
		t.Error("expected error for empty sub in ungrant")
	}
}
