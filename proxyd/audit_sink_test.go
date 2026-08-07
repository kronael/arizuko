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

// TestAuditSinkWritesRoutdDB proves proxyd's audit sink lands in routd.db,
// not the retired messages.db (BUGS.md Y1: proxyd owns no DB of its own,
// FS-mounted, writes owned tables into the owner DB — split write-discipline).
// Reproduces main()'s exact open sequence: routd boots first and creates
// routd.db (audit_log lives there per routd migration 0016), then proxyd
// opens the same file via store.OpenRoutd and wires audit.Init to it.
func TestAuditSinkWritesRoutdDB(t *testing.T) {
	dir := t.TempDir()

	// routd owns + migrates routd.db; it must boot at least once before
	// store.OpenRoutd will accept the directory (see store.OpenRoutd docs).
	rd, err := routd.Create(dir)
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
		Resource: "daemons/proxyd",
		Outcome:  audit.OutcomeOK,
	})

	var n int
	if err := stRoutd.DB().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='daemon.start' AND resource='daemons/proxyd'`,
	).Scan(&n); err != nil {
		t.Fatalf("read audit_log from routd.db: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log rows = %d, want 1 (proxyd's audit sink must write to routd.db)", n)
	}

	// messages.db must never be created by this path — proxyd opens exactly
	// one store (routd.db).
	if _, statErr := os.Stat(filepath.Join(dir, "messages.db")); statErr == nil {
		t.Fatalf("messages.db exists at %s — proxyd's audit path must not create it", dir)
	}
}
