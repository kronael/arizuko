package main

import (
	"os"
	"path/filepath"
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
			body := owner + suffix
			// routd.db has to be a real database: generate writes the release's
			// proxyd routes into it (BUGS L45), so it cannot double as a byte-
			// identity witness. A zero-byte file IS an empty SQLite DB. The
			// other three owners generate never opens, and they carry the
			// witness bytes.
			if owner == store.OwnerRoutd {
				body = ""
			}
			if err := os.WriteFile(filepath.Join(storeDir, file+suffix), []byte(body), 0o644); err != nil {
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
// from there the deploy that ships store/<owner>/ never happens.
func TestGenerateComposeMovesTheStoreLayout(t *testing.T) {
	dataDir := seedFlatInstance(t, "flat")
	storeDir := filepath.Join(dataDir, "store")

	generateCompose(dataDir)

	for owner, file := range store.OwnerDBs {
		for _, suffix := range []string{"", "-wal"} {
			// routd.db is a LIVE database by the time generate returns —
			// generate writes the release's proxyd routes into it (BUGS L45).
			// So it carries schema instead of the witness bytes, and SQLite
			// removes its -wal on the clean close. The move itself is still
			// asserted through routd.db; the other three owners, which generate
			// never opens, prove the bytes were carried and not recreated.
			live := owner == store.OwnerRoutd
			if live && suffix == "-wal" {
				continue
			}
			nested := store.OwnerDBPath(storeDir, owner) + suffix
			b, err := os.ReadFile(nested)
			if err != nil {
				t.Errorf("%s not in its owner directory: %v", file+suffix, err)
				continue
			}
			if got, want := string(b), owner+suffix; !live && got != want {
				t.Errorf("%s = %q, want %q", nested, got, want)
			}
			if _, err := os.Stat(filepath.Join(storeDir, file+suffix)); err == nil {
				t.Errorf("%s still sits flat in the store tree", file+suffix)
			}
		}
	}

}

// TestGenerateComposeStoreLayoutIdempotent: generate runs on every restart, so
// the second pass must find nothing to do and leave the tree byte-identical.
func TestGenerateComposeStoreLayoutIdempotent(t *testing.T) {
	dataDir := seedFlatInstance(t, "twice")
	storeDir := filepath.Join(dataDir, "store")

	// auth.db, not routd.db: generate legitimately writes the release's proxyd
	// routes into routd.db, so the untouched-bytes claim only holds for an owner
	// generate never opens.
	generateCompose(dataDir)
	nested := store.OwnerDBPath(storeDir, store.OwnerAuthd)
	before, err := os.Stat(nested)
	if err != nil {
		t.Fatal(err)
	}

	generateCompose(dataDir)
	after, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("auth.db gone after a second generate: %v", err)
	}
	// Inode, not mtime: a second-granular timestamp compare passes even when
	// the file was rewritten inside the same second.
	if !os.SameFile(before, after) {
		t.Error("the second generate replaced auth.db with a different file")
	}
	b, err := os.ReadFile(nested)
	if err != nil || string(b) != store.OwnerAuthd {
		t.Errorf("auth.db = %q (err %v), want it carried through untouched", b, err)
	}
}
