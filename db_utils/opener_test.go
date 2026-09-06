package db_utils

// Every daemon opens its database through this package. The pragma set is not
// advisory: modernc.org/sqlite defaults foreign-key enforcement OFF per
// connection, and database/sql pools many, so a site that writes its own DSN
// and forgets one pragma runs ON DELETE CASCADE as a no-op on most of its
// connections. Four sites had four different pragma strings, and one set WAL
// with a warning instead of an error — a session store silently in rollback
// journal mode looks identical to a healthy one.
//
// This test is the enforcement (spec 6/37, tier 1). It reads the tree rather
// than a list, so a new daemon is caught the day it is written.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openMemIsItsOwnDSN: store.OpenMem builds a per-call named in-memory DSN
// (`file:memdb-N?mode=memory&cache=shared`), which is not a file path and has
// no on-disk pragma set to share. It is the one legitimate direct open.
var openMemIsItsOwnDSN = map[string]bool{
	"store/store.go": true,
}

func TestEveryProductionOpenGoesThroughThisPackage(t *testing.T) {
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			// .claude holds agent worktrees and refs holds reference checkouts of
		// other projects; neither is this module's source.
		case ".git", ".claude", "refs", "node_modules", "tmp", "db_utils":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if openMemIsItsOwnDSN[filepath.ToSlash(rel)] {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, `sql.Open("sqlite"`) {
				t.Errorf("%s:%d opens SQLite directly — call db_utils.OpenWAL(db_utils.FileDSN(path)) "+
					"so the pragma set has one owner (spec 6/37)", filepath.ToSlash(rel), i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
