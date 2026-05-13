package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

func TestRunSecretSet_Folder(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	var buf bytes.Buffer
	if err := runSecretSet(s, store.ScopeFolder, "atlas/eng", "GITHUB_TOKEN", "ghp_xxx", &buf); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}
	if !strings.Contains(buf.String(), "set folder/atlas/eng/GITHUB_TOKEN") {
		t.Errorf("output = %q", buf.String())
	}
	got, err := s.GetSecret(store.ScopeFolder, "atlas/eng", "GITHUB_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "ghp_xxx" {
		t.Errorf("value = %q", got.Value)
	}
}

func TestRunSecretSet_RequiresValue(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	err := runSecretSet(s, store.ScopeFolder, "f", "KEY", "", new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "--value required") {
		t.Errorf("err = %v", err)
	}
}

func TestRunSecretSet_RejectsBadKey(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	for _, key := range []string{"lower", "1leading", "with-dash", "", " SPACE "} {
		err := runSecretSet(s, store.ScopeFolder, "f", key, "v", new(bytes.Buffer))
		if err == nil {
			t.Errorf("key=%q: want error", key)
		}
	}
}

func TestRunSecretList_Empty(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	var buf bytes.Buffer
	if err := runSecretList(s, store.ScopeFolder, "atlas", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no secrets") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestRunSecretList_Rows(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	s.SetSecret(store.ScopeFolder, "atlas", "A", "1")
	s.SetSecret(store.ScopeFolder, "atlas", "B", "2")
	var buf bytes.Buffer
	if err := runSecretList(s, store.ScopeFolder, "atlas", &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("missing keys in output: %q", out)
	}
	if strings.Contains(out, "1") && strings.Contains(out, "2") {
		// values must NOT appear in CLI list output.
		// Only headers + key + timestamp.
		// (timestamps may contain digits, so check stricter: literal "  1" or "  2" col)
		if strings.Contains(out, "\t1\t") || strings.Contains(out, "\t2\t") {
			t.Errorf("list leaked values: %q", out)
		}
	}
}

func TestRunSecretDelete(t *testing.T) {
	s, _ := store.OpenMem()
	defer s.Close()
	s.SetSecret(store.ScopeUser, "github:alice", "K", "v")
	var buf bytes.Buffer
	if err := runSecretDelete(s, store.ScopeUser, "github:alice", "K", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "deleted user/github:alice/K") {
		t.Errorf("output = %q", buf.String())
	}
	if _, err := s.GetSecret(store.ScopeUser, "github:alice", "K"); err == nil {
		t.Error("secret still present after delete")
	}
}

func TestKeyValid(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"GITHUB_TOKEN", true},
		{"K", true},
		{"A1_B2", true},
		{"", false},
		{"lower", false},
		{"1LEAD", false},
		{"WITH-DASH", false},
		{"WITH SPACE", false},
		{"a_lower", false},
	}
	for _, c := range cases {
		if got := keyValid(c.key); got != c.want {
			t.Errorf("keyValid(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}
