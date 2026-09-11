package db_utils

// Every daemon opens its database through this package. The pragma set is not
// advisory: modernc.org/sqlite defaults foreign-key enforcement OFF per
// connection, and database/sql pools many, so a site that writes its own DSN
// and forgets one pragma runs ON DELETE CASCADE as a no-op on most of its
// connections. Four sites had four different pragma strings, and one set WAL
// with a warning instead of an error — a session store silently in rollback
// journal mode looks identical to a healthy one.
//
// A test file is held to the same rule, and for a second reason: forty test
// files each writing their own SQLite DSN is forty places to edit before the
// suite can be pointed at another engine, which is the proof 6/37 step 6 asks
// for. db_utils/dbtest is a test's one way in, and it takes its DSN from this
// package like every daemon does.
//
// This test is the enforcement (spec 6/37, tier 1). It reads the tree rather
// than a list, so a new daemon — or a new test — is caught the day it is
// written.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postgresOpens are the ways a caller reaches Postgres past db_utils.Open. Each
// one skips the connection cap, the exec mode a transaction-mode pooler
// requires, and the transient class — the three things this package owns on the
// Postgres side, and none of them visible in a passing test.
var postgresOpens = []string{
	`sql.Open("pgx"`,
	`sql.Open("pgx/v5"`,
	`sql.Open("postgres"`,
	"stdlib.OpenDB(",
	"stdlib.OpenDBFromDSN(",
	"stdlib.RegisterConnConfig(",
	"pgxpool.New(",
	"pgxpool.NewWithConfig(",
	"pgx.Connect(",
	"pgx.ConnectConfig(",
}

func TestEveryOpenGoesThroughThisPackage(t *testing.T) {
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// .claude holds agent worktrees and refs holds reference checkouts of
			// other projects; neither is this module's source.
			switch info.Name() {
			case ".git", ".claude", "refs", "node_modules", "tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		// This package IS the opener, so it is the one place that names a driver.
		// The exemption is these files alone: db_utils/dbtest is a separate package
		// and answers to the rule like everything else.
		if filepath.Dir(rel) == "db_utils" {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, `sql.Open("sqlite"`) {
				t.Errorf("%s:%d opens SQLite directly — call db_utils.Open(target) "+
					"for a configured database, or dbtest.Open(t) for a test database, so the "+
					"pragma set and the engine have one owner (spec 6/37)", filepath.ToSlash(rel), i+1)
			}
			for _, open := range postgresOpens {
				if strings.Contains(line, open) {
					t.Errorf("%s:%d opens Postgres directly — call db_utils.Open(dsn) so the "+
						"connection cap, the query exec mode and the transient class have one "+
						"owner (spec 6/37)", filepath.ToSlash(rel), i+1)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
