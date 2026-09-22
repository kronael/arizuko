package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func packagesDB(t *testing.T) objstore.Store { return routdDB(t) }

func packagesGet(t *testing.T, db objstore.Store) string {
	t.Helper()
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/packages/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	return w.Body.String()
}

// TestPackagesPageEmpty: no installed packages renders the empty marker.
func TestPackagesPageEmpty(t *testing.T) {
	db := packagesDB(t)
	if body := packagesGet(t, db); !strings.Contains(body, "No packages installed") {
		t.Errorf("empty packages page missing marker: %s", body)
	}
}

// TestPackagesPageRows: an installed package renders its columns.
func TestPackagesPageRows(t *testing.T) {
	db := packagesDB(t)
	seedInstalledPackage(t, db, "", "slakd", "github.com/org/slakd", "abc123",
		"2026-07-29T00:00:00Z")
	body := packagesGet(t, db)
	for _, want := range []string{"slakd", "github.com/org/slakd", "abc123"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in packages rows: %s", want, body)
		}
	}
}

// TestPackagesPageShowsScope: the record is keyed (folder, name) and both kinds
// of row live in one table — an instance-wide package and a product blended into
// one group (spec 5/28-packages). Without the folder column an operator reads a group's
// product as something that installed a sidecar instance-wide.
func TestPackagesPageShowsScope(t *testing.T) {
	db := packagesDB(t)
	seedInstalledPackage(t, db, "", "slakd", "github.com/org/slakd", "abc123",
		"2026-07-29T00:00:00Z")
	seedInstalledPackage(t, db, "atlas/support", "trip", "ant/examples/trip", "local",
		"2026-08-07T00:00:00Z")
	body := packagesGet(t, db)
	if !strings.Contains(body, "<code>atlas/support</code>") {
		t.Errorf("group-scoped product does not show its folder: %s", body)
	}
	if !strings.Contains(body, `<span class="dim">instance</span>`) {
		t.Errorf("instance-wide package does not say so: %s", body)
	}
}

// seedInstalledPackage lands one install record. routd writes these items, so
// the fixture addresses them the way routd does — (folder, name) is the
// coordinate and the two document attributes hold their empty document, never
// null.
func seedInstalledPackage(t *testing.T, db objstore.Store, folder, name, source, revision, at string) {
	t.Helper()
	key, err := store.InstalledPackageKey(folder, name)
	if err != nil {
		t.Fatalf("coordinate of %s/%s: %v", folder, name, err)
	}
	if _, err := db.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: store.InstalledPackages, Key: key,
		Attrs: map[string]string{
			store.AttrPkgSource:      source,
			store.AttrPkgRevision:    revision,
			store.AttrPkgInstalledAt: at,
			store.AttrPkgManifest:    "{}",
			store.AttrPkgAssetHashes: "{}",
		},
	}}); err != nil {
		t.Fatalf("seed installed package %s/%s: %v", folder, name, err)
	}
}
