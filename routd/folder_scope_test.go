package routd

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// TestFolderScope_ClassifiesEveryRoutdTable is the twin of store's completeness
// test against the schema the group delete ACTUALLY runs on: `arizuko group rm`,
// dashd's delete page and routd's registration rollback all open routd.db, and
// it holds tables messages.db does not. One declaration, two readers.
func TestFolderScope_ClassifiesEveryRoutdTable(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.SQL().Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !store.FolderScopeClassified(name) {
			missing = append(missing, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("routd.db tables with no folder-scope verdict: %s\n"+
			"add each to folderScopePurge, folderScopeKeep or folderScopeUnpurged in store/folder_scope.go",
			strings.Join(missing, ", "))
	}
}
