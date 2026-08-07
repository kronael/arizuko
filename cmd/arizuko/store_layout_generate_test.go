package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// seedFlatInstance builds an instance whose store/ is still FLAT — the shape
// every live instance has before this ships.
func seedFlatInstance(t *testing.T, name string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("HOST_APP_DIR", "")
	dataDir := filepath.Join(base, "arizuko_"+name)
	storeDir := filepath.Join(dataDir, "store")
	for _, d := range []string{storeDir, filepath.Join(dataDir, "services")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for owner, file := range store.OwnerDBs {
		for _, suffix := range []string{"", "-wal"} {
			if err := os.WriteFile(filepath.Join(storeDir, file+suffix), []byte(owner+suffix), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte("API_PORT=8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

// TestGenerateComposeMovesTheStoreLayout is the WIRING assertion, not the
// mover's: `arizuko generate` is what a live instance runs on every restart
// (systemd ExecStartPre, after `compose down`), so if the move is not called
// from there the deploy that ships store/<owner>/ takes the fleet down — every
// owner daemon now refuses to boot on a missing file rather than creating one.
//
// It also pins the ORDER. The compose written in the same call binds
// store/<owner> and points the DB path env vars inside it; a compose produced
// before the tree moved is the outage this exists to prevent.
func TestGenerateComposeMovesTheStoreLayout(t *testing.T) {
	dataDir := seedFlatInstance(t, "flat")
	storeDir := filepath.Join(dataDir, "store")

	outPath := generateCompose(dataDir)

	for owner, file := range store.OwnerDBs {
		for _, suffix := range []string{"", "-wal"} {
			nested := store.OwnerDBPath(storeDir, owner) + suffix
			b, err := os.ReadFile(nested)
			if err != nil {
				t.Errorf("%s not in its owner directory: %v", file+suffix, err)
				continue
			}
			if got, want := string(b), owner+suffix; got != want {
				t.Errorf("%s = %q, want %q", nested, got, want)
			}
			if _, err := os.Stat(filepath.Join(storeDir, file+suffix)); err == nil {
				t.Errorf("%s still sits flat in the store tree", file+suffix)
			}
		}
	}

	// The compose emitted by this same call must name the directories the move
	// just produced — the two halves are one change or they are an outage.
	yml, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	for owner := range store.OwnerDBs {
		want := storeDir + "/" + owner + ":"
		if owner == store.OwnerRuned {
			continue // runed takes the whole tree; it has no per-owner bind
		}
		if !strings.Contains(string(yml), want) {
			t.Errorf("generated compose has no %q bind", want)
		}
	}
}

// TestGenerateComposeStoreLayoutIdempotent: generate runs on every restart, so
// the second pass must find nothing to do and leave the tree byte-identical.
func TestGenerateComposeStoreLayoutIdempotent(t *testing.T) {
	dataDir := seedFlatInstance(t, "twice")
	storeDir := filepath.Join(dataDir, "store")

	generateCompose(dataDir)
	nested := store.OwnerDBPath(storeDir, store.OwnerRoutd)
	before, err := os.Stat(nested)
	if err != nil {
		t.Fatal(err)
	}

	generateCompose(dataDir)
	after, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("routd.db gone after a second generate: %v", err)
	}
	// Inode, not mtime: a second-granular timestamp compare passes even when
	// the file was rewritten inside the same second.
	if !os.SameFile(before, after) {
		t.Error("the second generate replaced routd.db with a different file")
	}
	b, err := os.ReadFile(nested)
	if err != nil || string(b) != store.OwnerRoutd {
		t.Errorf("routd.db = %q (err %v), want it carried through untouched", b, err)
	}
}
