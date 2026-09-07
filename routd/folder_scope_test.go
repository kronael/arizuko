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

// TestFolderScope_EveryClaimedCascadeExists is what BUGS L253 asks for: a keep
// verdict that names a MECHANISM has to be checked, because the mechanism can
// go away while the verdict stays.
//
// Three tables are kept on the ground that the groups row's ON DELETE CASCADE
// removes them. An objstore entity is three columns and no foreign keys, and a
// key-value engine has no cascade at all — so the day one of those tables moves,
// its rows are deleted by NOTHING while the completeness test above still
// passes, because the table is still classified. Two of the three are live
// ingress credentials.
//
// It runs HERE and not beside the declaration because routd.db is the schema the
// group delete actually runs on; store's frozen chain builds a database no
// daemon opens, and asserting against that is the mistake BUGS Q1 records.
//
// What this reads is whether the constraint EXISTS. That SQLite then honours it
// is proved separately, by the behavioural tests in store/fk_test.go.
func TestFolderScope_EveryClaimedCascadeExists(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for table, parent := range store.FolderScopeCascades() {
		var n int
		if err := db.SQL().QueryRow(
			`SELECT COUNT(*) FROM pragma_foreign_key_list(?)
			 WHERE "table" = ? AND on_delete = 'CASCADE'`, table, parent).Scan(&n); err != nil {
			t.Fatalf("read %s's foreign keys: %v", table, err)
		}
		if n == 0 {
			t.Errorf("folderScopeKeep keeps %s because %s's delete cascades to it, and "+
				"routd.db declares no such cascade — so the group delete removes nothing "+
				"there. Either the constraint is gone or the table is an objstore entity "+
				"now; a table with no cascade belongs in folderScopePurge with a delete of "+
				"its own (BUGS L253)", table, parent)
		}
	}
}
