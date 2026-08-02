package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// TestAuditSinkWritesRoutdDB proves webd's audit sink lands in routd.db and
// that webd opens exactly ONE store (specs/5/16 § "One owner + federation":
// webd owns no DB, is FS-mounted, and writes owned tables into the owner DB).
// Reproduces main()'s open sequence: routd boots first and creates routd.db
// (audit_log lives there per routd migration 0016), then webd opens the same
// file via store.OpenRoutd and wires audit.Init to it — no store.Open, so
// messages.db is never created.
func TestAuditSinkWritesRoutdDB(t *testing.T) {
	dir := t.TempDir()

	// routd owns + migrates routd.db; it must boot at least once before
	// store.OpenRoutd will accept the directory (see store.OpenRoutd docs).
	rd, err := routd.Open(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	rd.Close()

	stRoutd, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("store.OpenRoutd: %v", err)
	}
	defer stRoutd.Close()

	audit.Init(stRoutd.DB(), "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceREST,
		Resource: "daemons/webd",
		Outcome:  audit.OutcomeOK,
	})

	var n int
	if err := stRoutd.DB().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='daemon.start' AND resource='daemons/webd'`,
	).Scan(&n); err != nil {
		t.Fatalf("read audit_log from routd.db: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log rows = %d, want 1 (webd's audit sink must write to routd.db)", n)
	}

	// messages.db must never be created by this path — webd opens exactly one
	// store (routd.db).
	if _, statErr := os.Stat(filepath.Join(dir, "messages.db")); statErr == nil {
		t.Fatalf("messages.db exists at %s — webd must not open the retired messages.db", dir)
	}
}
