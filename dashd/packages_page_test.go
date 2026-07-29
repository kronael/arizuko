package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func packagesDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE installed_packages (
		name TEXT PRIMARY KEY, source TEXT NOT NULL, revision TEXT NOT NULL,
		manifest TEXT NOT NULL DEFAULT '{}', asset_hashes TEXT NOT NULL DEFAULT '{}',
		installed_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func packagesGet(t *testing.T, db *sql.DB) string {
	t.Helper()
	d := &dash{db: db, dbRoutd: db}
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
	defer db.Close()
	if body := packagesGet(t, db); !strings.Contains(body, "No packages installed") {
		t.Errorf("empty packages page missing marker: %s", body)
	}
}

// TestPackagesPageRows: an installed package renders its columns.
func TestPackagesPageRows(t *testing.T) {
	db := packagesDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO installed_packages(name, source, revision, installed_at)
		VALUES('slakd','github.com/org/slakd','abc123','2026-07-29T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	body := packagesGet(t, db)
	for _, want := range []string{"slakd", "github.com/org/slakd", "abc123"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in packages rows: %s", want, body)
		}
	}
}
