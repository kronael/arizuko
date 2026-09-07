package dashstore

import (
	"testing"

	"github.com/kronael/arizuko/routd"
)

// RoutesIntoFolder scopes the dashboard's route panel on `target`. A folder
// name may hold `_` and `%` and SQL LIKE folds ASCII case, so the predicate is
// substr — a LIKE here shows a neighbour's routes on an operator's page
// (BUGS L252).
func TestRoutesIntoFolderIsNotWidenedByLikeMetacharacters(t *testing.T) {
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	db := d.SQL()
	for i, target := range []string{"a_b", "a_b/one", "aXb", "aXb/two"} {
		if _, err := db.Exec(
			`INSERT INTO routes(seq, match, target) VALUES(?,?,?)`,
			i+1, "platform=slack", target); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(db).RoutesIntoFolder("a_b")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("RoutesIntoFolder(a_b) returned %d routes, want 2 (a_b and a_b/one)", len(got))
	}
	for _, g := range got {
		if g.Target != "a_b" && g.Target != "a_b/one" {
			t.Errorf("RoutesIntoFolder(a_b) returned %q, which is not in a_b's subtree", g.Target)
		}
	}
}
