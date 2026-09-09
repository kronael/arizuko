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
