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
// sqlLayers is the covered set. dashd is the only daemon that has moved; the
// others still hold statements in their handlers and would fail this test for
// work nobody has started, so they are added one line at a time as each one
// moves. When the second daemon lands, this test belongs somewhere neither
// daemon owns.

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
// SQL, and the storage package inside it that holds all of it.
type sqlLayer struct {
	daemon  string // directory, relative to the repo root
	storage string // the one package under it that may run statements
}

var sqlLayers = []sqlLayer{
	{daemon: "dashd", storage: "dashd/dashstore"},
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
