package dashstore

import (
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// RoutesIntoFolder scopes the dashboard's route panel on `target`. A folder
// name may hold `_` and `%`, and a predicate that treats either as a wildcard
// shows a neighbour's routes on an operator's page (BUGS L252). The names here
// are exactly that pair.
func TestRoutesIntoFolderIsNotWidenedByLikeMetacharacters(t *testing.T) {
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	db := d.Obj()
	for i, target := range []string{"a_b", "a_b/one", "aXb", "aXb/two"} {
		if _, err := store.NewOver(db).PutRouteRow(core.Route{
			Seq: i + 1, Match: "platform=slack", Target: target,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store.NewOver(db).Obj()).RoutesIntoFolder("a_b")
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
