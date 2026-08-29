package auth

import (
	"slices"
	"testing"
)

func TestShellEnvKeys_NoRowGrantsNothing(t *testing.T) {
	s := openMem(t)
	got := ShellEnvKeys(s, Caller{Principal: "folder:atlas"}, "atlas",
		[]string{"GITHUB_TOKEN", "BUILDKITE_TOKEN"})
	if len(got) != 0 {
		t.Fatalf("no grant row must grant nothing, got %v", got)
	}
}

func TestShellEnvKeys_NamedKeyOnly(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", ShellEnvPrefix+"GITHUB_TOKEN", "atlas", "allow")
	got := ShellEnvKeys(s, Caller{Principal: "folder:atlas"}, "atlas",
		[]string{"GITHUB_TOKEN", "BUILDKITE_TOKEN"})
	if !slices.Equal(got, []string{"GITHUB_TOKEN"}) {
		t.Fatalf("a grant on one key must not carry the other, got %v", got)
	}
}

func TestShellEnvKeys_StarGrantsAll(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", ShellEnvPrefix+"*", "atlas", "allow")
	got := ShellEnvKeys(s, Caller{Principal: "folder:atlas"}, "atlas",
		[]string{"GITHUB_TOKEN", "BUILDKITE_TOKEN"})
	if len(got) != 2 {
		t.Fatalf("shell:env:* must grant every candidate, got %v", got)
	}
}

func TestShellEnvKeys_DenyWins(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas", ShellEnvPrefix+"*", "atlas", "allow")
	addRow(t, s, "folder:atlas", ShellEnvPrefix+"BUILDKITE_TOKEN", "atlas", "deny")
	got := ShellEnvKeys(s, Caller{Principal: "folder:atlas"}, "atlas",
		[]string{"GITHUB_TOKEN", "BUILDKITE_TOKEN"})
	if !slices.Equal(got, []string{"GITHUB_TOKEN"}) {
		t.Fatalf("deny must win over shell:env:*, got %v", got)
	}
}

// TestShellEnvKeys_OperatorStarGrantsNothing is the CheckHold hazard again. An
// operator's (*, **) row covers every action under Authorize's lattice, so
// routing this through Authorize would drop every folder secret into every
// operator turn's env — the widest blast radius the grant could have. The exact
// action match is what stops it, so this test is the reason the loop is hand-rolled.
func TestShellEnvKeys_OperatorStarGrantsNothing(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "role:operator", "*", "**", "allow")
	if err := s.AddMembership("folder:atlas", "role:operator", "test"); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	if Authorize(s, Caller{Principal: "folder:atlas"}, "anything", "atlas", nil) != true {
		t.Fatal("precondition: the operator row must cover an arbitrary action")
	}
	got := ShellEnvKeys(s, Caller{Principal: "folder:atlas"}, "atlas", []string{"GITHUB_TOKEN"})
	if len(got) != 0 {
		t.Fatalf("an operator lattice row must not grant a shell credential, got %v", got)
	}
}

// TestShellEnvKeys_ScopeContains pins that the grant is folder-scoped like every
// other row: a rule written at atlas covers atlas/eng and nothing outside atlas.
func TestShellEnvKeys_ScopeContains(t *testing.T) {
	s := openMem(t)
	addRow(t, s, "folder:atlas/eng", ShellEnvPrefix+"GITHUB_TOKEN", "atlas/**", "allow")
	if got := ShellEnvKeys(s, Caller{Principal: "folder:atlas/eng"}, "atlas/eng",
		[]string{"GITHUB_TOKEN"}); len(got) != 1 {
		t.Fatalf("atlas/** must cover atlas/eng, got %v", got)
	}
	if got := ShellEnvKeys(s, Caller{Principal: "folder:atlas/eng"}, "other",
		[]string{"GITHUB_TOKEN"}); len(got) != 0 {
		t.Fatalf("atlas/** must not cover other, got %v", got)
	}
}
