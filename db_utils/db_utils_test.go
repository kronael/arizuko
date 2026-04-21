package db_utils

import (
	"database/sql"
	"embed"
	"testing"

	_ "modernc.org/sqlite"
)

//go:embed testdata/*.sql testdata/*.md
var testFS embed.FS

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// First migration creates the announcements table; second migration has a
// paired .md; third does not.
func TestMigrate_AnnouncementsRow(t *testing.T) {
	db := openMem(t)
	if err := Migrate(db, testFS, "testdata", "test"); err != nil {
		t.Fatal(err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM announcements`).Scan(&n)
	if n != 1 {
		t.Fatalf("announcements rows: got %d, want 1", n)
	}

	var service, body string
	var version int
	if err := db.QueryRow(
		`SELECT service, version, body FROM announcements`,
	).Scan(&service, &version, &body); err != nil {
		t.Fatal(err)
	}
	if service != "test" || version != 2 {
		t.Fatalf("row: service=%q version=%d", service, version)
	}
	if body == "" {
		t.Fatal("body empty")
	}
}
