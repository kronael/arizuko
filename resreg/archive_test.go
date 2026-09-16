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
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // side-effect: register cold-tier resources
	"github.com/kronael/arizuko/store"
)

func archiveTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
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
	st := archiveTestStore(t)
	ctx := context.Background()
	if err := st.PutGroupRow(core.Group{Folder: "atlas", AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	manifest, checksum, snapshotAt, err := resreg.ExportSnapshot(ctx, resreg.On(st.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if manifest == nil {
		t.Error("manifest nil")
	}
	if snapshotAt == "" {
		t.Error("snapshotAt empty")
	}
	want, err := resreg.Checksum(resreg.On(st.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if checksum != want {
		t.Errorf("checksum = %s, want %s (resreg.Checksum())", checksum, want)
	}
}
