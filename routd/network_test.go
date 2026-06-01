package routd

import (
	"slices"
	"testing"
)

// TestResolveAllowlist: a folder inherits the instance base (folder='') seeded by
// the migration plus every ancestor's network_rules, de-duped + sorted.
func TestResolveAllowlist(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// base seed (anthropic.com, api.anthropic.com at folder='') comes from the
	// migration. Add a world rule + a leaf rule.
	for _, r := range []struct{ folder, target string }{
		{"acme", "acme.com"},
		{"acme/eng", "github.com"},
	} {
		if _, err := db.SQL().Exec(
			`INSERT INTO network_rules(folder, target, created_at) VALUES(?,?,?)`,
			r.folder, r.target, nowTS()); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ResolveAllowlist("acme/eng")
	if err != nil {
		t.Fatalf("ResolveAllowlist: %v", err)
	}
	// base ('') + acme + acme/eng, all inherited.
	for _, want := range []string{"anthropic.com", "api.anthropic.com", "acme.com", "github.com"} {
		if !slices.Contains(got, want) {
			t.Errorf("allowlist %v missing %q (ancestry inheritance broken)", got, want)
		}
	}

	// a sibling folder must NOT see acme/eng's leaf rule.
	other, _ := db.ResolveAllowlist("acme/ops")
	if slices.Contains(other, "github.com") {
		t.Errorf("acme/ops leaked acme/eng rule: %v", other)
	}
}
