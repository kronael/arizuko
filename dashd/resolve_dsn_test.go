package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveDSN_DefaultsToRoutdDB: with no DB_PATH, dashd's store is routd.db
// under ITS OWNER's subdirectory — the only store/ path dashd's mount carries
// for the tables it reads (spec 5/16 step 7). The pre-split messages.db is
// retired and stays flat.
func TestResolveDSN_DefaultsToRoutdDB(t *testing.T) {
	got, err := resolveDSN("", "/srv/data/arizuko_x")
	if err != nil {
		t.Fatalf("resolveDSN: %v", err)
	}
	want := filepath.Join("/srv/data/arizuko_x", "store", "routd", "routd.db")
	if got != want {
		t.Errorf("dsn = %q, want %q", got, want)
	}
}

// TestResolveDSN_RejectsMonolith: an explicit DB_PATH pointing at the retired
// monolith must fail loudly at boot. A stale value left in an old deployment's
// env would otherwise make dashd serve frozen pre-split data with no error —
// exactly the silent-staleness this retirement removes.
func TestResolveDSN_RejectsMonolith(t *testing.T) {
	for _, p := range []string{
		"/srv/app/home/store/messages.db",
		"messages.db",
	} {
		if _, err := resolveDSN(p, "/srv/data/arizuko_x"); err == nil {
			t.Errorf("resolveDSN(%q) = nil error, want refusal", p)
		} else if !strings.Contains(err.Error(), "routd.db") {
			t.Errorf("resolveDSN(%q) error %q should name the replacement", p, err)
		}
	}
}

// TestResolveDSN_HonoursExplicitPath: a non-monolith DB_PATH is taken verbatim,
// and the onbod/runed siblings resolve off its directory.
func TestResolveDSN_HonoursExplicitPath(t *testing.T) {
	got, err := resolveDSN("/custom/store/routd.db", "")
	if err != nil {
		t.Fatalf("resolveDSN: %v", err)
	}
	if got != "/custom/store/routd.db" {
		t.Errorf("dsn = %q, want the explicit path", got)
	}
}

// TestResolveDSN_RequiresConfig: neither DB_PATH nor DATA_DIR is a fatal misconfig.
func TestResolveDSN_RequiresConfig(t *testing.T) {
	if _, err := resolveDSN("", ""); err == nil {
		t.Error("resolveDSN with no config = nil error, want failure")
	}
}
