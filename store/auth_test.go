package store

import (
	"testing"
)

func TestCanonicalSubUnknownPassesThrough(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := s.CanonicalSub("google:nobody"); got != "google:nobody" {
		t.Fatalf("unknown sub should pass through, got %q", got)
	}
}

func TestCanonicalSubCanonicalSelf(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateAuthUser("google:alice", "alice", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	if got := s.CanonicalSub("google:alice"); got != "google:alice" {
		t.Fatalf("canonical self should return self, got %q", got)
	}
}

func TestLinkSubAndCanonicalResolve(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateAuthUser("google:alice", "alice", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSubToCanonical("github:alice2", "Alice GH", "google:alice"); err != nil {
		t.Fatal(err)
	}
	if got := s.CanonicalSub("github:alice2"); got != "google:alice" {
		t.Fatalf("linked sub should resolve to canonical, got %q", got)
	}
	if got := s.CanonicalSub("google:alice"); got != "google:alice" {
		t.Fatalf("canonical self unaffected, got %q", got)
	}

	// Idempotent re-link
	if err := s.LinkSubToCanonical("github:alice2", "Alice GH", "google:alice"); err != nil {
		t.Fatalf("re-link of same pair should be idempotent: %v", err)
	}

	// LinkedSubs lists the link
	got := s.LinkedSubs("google:alice")
	if len(got) != 1 || got[0] != "github:alice2" {
		t.Fatalf("LinkedSubs got %v, want [github:alice2]", got)
	}
}

func TestLinkSubRejectsChain(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSubToCanonical("github:b", "B", "google:a"); err != nil {
		t.Fatal(err)
	}
	// Linking to a non-canonical sub must fail.
	if err := s.LinkSubToCanonical("discord:c", "C", "github:b"); err == nil {
		t.Fatal("linking to a non-canonical sub should fail")
	}
}

func TestLinkSubRejectsSelfAndUnknownCanonical(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.LinkSubToCanonical("x", "X", "x"); err == nil {
		t.Fatal("linking sub to itself should fail")
	}
	if err := s.LinkSubToCanonical("github:new", "New", "google:does-not-exist"); err == nil {
		t.Fatal("linking to unknown canonical should fail")
	}
}

func TestLinkSubUpdatesExistingNewSub(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthUser("github:b", "b", "", "B"); err != nil {
		t.Fatal(err)
	}
	// b exists as canonical; now point it at google:a.
	if err := s.LinkSubToCanonical("github:b", "B updated", "google:a"); err != nil {
		t.Fatal(err)
	}
	if got := s.CanonicalSub("github:b"); got != "google:a" {
		t.Fatalf("github:b should now resolve to google:a, got %q", got)
	}
	u, ok := s.AuthUserBySub("github:b")
	if !ok || u.LinkedToSub != "google:a" {
		t.Fatalf("github:b linked_to_sub got %q, want google:a", u.LinkedToSub)
	}
}
