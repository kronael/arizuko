package db_utils

// A daemon holds no SQL. Every statement it runs lives in its own storage
// package, so a second driver reimplements that one package and no handler
// changes (spec 6/37 step 1). This test is the enforcement, and it sits beside
// TestEveryOpenGoesThroughThisPackage because both guard the same seam: that
// one names the engine's only opener, this one names the only place a
// statement may appear.
//
// It reads string literals through go/parser rather than raw lines. A comment
// that quotes a query is prose, not a statement — dashd carries two, and a
// line-based reader would have to be lied to about them.
//
// sqlLayers is the covered set. Daemons are added one line at a time as each
// one moves; an uncovered daemon still holds statements in its handlers and
// would fail this test for work nobody has started.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sqlLayer is one daemon that has moved: the package tree that must hold no
// SQL, and the one place inside it that holds all of it. That place is a
// package where the daemon grew large enough to need one, and a single file
// where it did not — the guarantee is the same either way, that a second
// driver rewrites one named thing and no handler changes.
type sqlLayer struct {
	daemon  string // directory, relative to the repo root
	storage string // the one package or file under it that may run statements
}

// A daemon that holds NO statement at all names a storage path that does not
// exist, so the walk skips nothing and the whole tree is checked. emaid is the
// first: its storage is emaid/mailstore, which is objstore's two primitives and
// no SQL, so there is no place left to exempt.
//
// chanlib is not a daemon and it is here for the same reason: its storage is
// chanlib/chanstore, which holds no SQL either, and the entities it ships land
// in EIGHT adapter databases. A statement that reappeared in chanlib would put
// itself in all of them at once.
var sqlLayers = []sqlLayer{
	{daemon: "authd", storage: "authd/authstore"},
	{daemon: "chanlib", storage: "chanlib/none"},
	{daemon: "dashd", storage: "dashd/dashstore"},
	{daemon: "emaid", storage: "emaid/none"},
	{daemon: "runed", storage: "runed/db.go"},
}

// sqlVerbs open a statement. A literal is SQL when one of these starts it or
// starts a line inside it, which is how every statement in the tree is written
// — the fragment concatenated onto one (an IN-list's placeholders, a trailing
// GROUP BY) is caught through the fragment that carries the verb.
var sqlVerbs = []string{
	"SELECT ",
	"INSERT INTO ",
	"INSERT OR ",
	"UPDATE ",
	"DELETE FROM ",
	"CREATE TABLE ",
	"CREATE INDEX ",
	"DROP TABLE ",
	"ALTER TABLE ",
	"WITH RECURSIVE ",
}

func TestSQLLivesOnlyInTheStorageLayer(t *testing.T) {
	root := ".."
	for _, layer := range sqlLayers {
		dir := filepath.Join(root, layer.daemon)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s: %v", layer.daemon, err)
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			if info.IsDir() {
				if rel == layer.storage {
					return filepath.SkipDir
				}
				return nil
			}
			if rel == layer.storage {
				return nil
			}
			// A test builds the state it reads, so its fixtures write rows
			// directly. That is the test-side seam, tracked on its own.
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for _, line := range sqlLiteralLines(t, path) {
				t.Errorf("%s:%d holds SQL outside %s — move the statement into a "+
					"method named for what the caller needs, so a second driver can "+
					"answer the same question differently (spec 6/37 step 1): %s",
					rel, line.line, layer.storage, line.text)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", layer.daemon, err)
		}
	}
}

type sqlHit struct {
	line int
	text string
}

// sqlLiteralLines reports every string literal in the file that opens a
// statement, with the line it sits on.
func sqlLiteralLines(t *testing.T, path string) []sqlHit {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var hits []sqlHit
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if verb := sqlVerb(v); verb != "" {
			hits = append(hits, sqlHit{line: fset.Position(lit.Pos()).Line, text: verb})
		}
		return true
	})
	return hits
}

// sqlVerb returns the verb that opens the literal or one of its lines, empty
// when the literal is not a statement.
func sqlVerb(s string) string {
	for _, line := range strings.Split(s, "\n") {
		up := strings.ToUpper(strings.TrimLeft(line, " \t"))
		for _, verb := range sqlVerbs {
			if strings.HasPrefix(up, verb) {
				return strings.TrimSpace(verb)
			}
		}
	}
	return ""
}
