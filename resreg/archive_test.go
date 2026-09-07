// Package resreg_test (external) so it can blank-import resreg/resources
// for its resource registrations — resreg/resources imports resreg, and an
// INTERNAL resreg test file cannot import a package that imports resreg
// back (a cycle at the test-binary level); the external test package
// convention is exactly the escape hatch for that.
package resreg_test

// Archive index tests — spec 5/8 "The full-instance archive". What is left in
// this file is the part of the archive that is not any one table: the
// subsystem snapshot and the checksum it stamps. Each table's own export and
// import lane is a method on the owner of that table (store/route_tokens.go,
// store/invites.go, store/onboarding.go, store/messages.go), tested there.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/db_utils/dbtest"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/store"
)

func archiveTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Open(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestExportSnapshot_MatchesChecksum does NOT assert on any specific
// resource key being present in the manifest: other tests in this package
// binary (engine_test.go, openapi_test.go) call the package-private reset()
// between their own runs, and Go gives no ordering guarantee between an
// internal (package resreg) and external (package resreg_test) test file's
// registrations — so which resources are registered when THIS test runs is
// not something to depend on. What IS registry-order-independent, and is
// the actual thing worth proving, is that ExportSnapshot's one-read-tx path
// computes the SAME checksum Checksum() does against whatever the live
// registry holds at call time — both read it fresh, so they must always
// agree.
func TestExportSnapshot_MatchesChecksum(t *testing.T) {
	db := archiveTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO groups(folder, added_at) VALUES('atlas', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	manifest, checksum, snapshotAt, err := resreg.ExportSnapshot(ctx, resreg.OnDB(db), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if manifest == nil {
		t.Error("manifest nil")
	}
	if snapshotAt == "" {
		t.Error("snapshotAt empty")
	}
	want, err := resreg.Checksum(resreg.OnDB(db), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if checksum != want {
		t.Errorf("checksum = %s, want %s (resreg.Checksum())", checksum, want)
	}
}
