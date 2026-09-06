package store

import (
	"strings"
	"testing"
)

// SchemaTables lists every table the pool's schema holds. The completeness tests
// walk it; routd has its own because its schema is not this one.
func SchemaTables(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

// TestFolderScope_ClassifiesEverySchemaTable is what makes "the purge is
// complete" checkable rather than asserted. Every table carries a verdict:
// purged, kept with a reason, or named as an unpurged folder coordinate. A table
// joining the schema arrives here first, so the question of whether it confers
// on a folder cannot be skipped the way it was for acl (BUGS L198).
func TestFolderScope_ClassifiesEverySchemaTable(t *testing.T) {
	s := openFKStore(t)
	var missing []string
	for _, table := range SchemaTables(t, s) {
		if !FolderScopeClassified(table) {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("tables with no folder-scope verdict: %s\n"+
			"add each to folderScopePurge, folderScopeKeep or folderScopeUnpurged in store/folder_scope.go",
			strings.Join(missing, ", "))
	}
}

// TestFolderScope_EveryPurgeScopeRuns pins the declaration against the schema:
// a scope naming a column the schema lacks would fail every delete, and a scope
// whose SQL is malformed would too. Executing each statement is the only way to
// learn that, since nothing else parses them.
func TestFolderScope_EveryPurgeScopeRuns(t *testing.T) {
	s := openFKStore(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	for _, f := range folderScopePurge {
		q, args := f.stmt("acme")
		if _, err := tx.Exec(q, args...); err != nil {
			t.Errorf("%s.%s: %v\n%s", f.table, f.col, err, q)
		}
	}
}

// TestFolderScope_SubtreeIsExact: the predicate is `substr`, not `LIKE`, so `_`
// and `%` are literals and the compare is case-sensitive. A folder named `a_b`
// deleting `axb`'s rows is BUGS L122, and every scope here shares the one
// renderer, so one scope proves the shape.
func TestFolderScope_SubtreeIsExact(t *testing.T) {
	q, args := folderScope{table: "acl", col: "scope", seps: []string{"/"}}.stmt("a_b")
	if strings.Contains(strings.ToUpper(q), "LIKE") {
		t.Fatalf("subtree predicate uses LIKE: %s", q)
	}
	if len(args) != 3 || args[2] != "a_b/" {
		t.Fatalf("args = %v, want the folder, its prefix length and `a_b/`", args)
	}
}
