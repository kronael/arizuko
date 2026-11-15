package routd

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/kronael/arizuko/core"
)

// The scope string anteval's lifecycle mints for an eval bearer decides whether
// the runner may write the eval folder's harness: without a routes:write scope
// the update is refused, with routes:write:own_group it reaches only that
// folder's subtree.
func TestEvalBearerHarnessUpdate(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, folder := range []string{"eval", "other"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name  string
		scope []string
		want  map[string]int
	}{
		{"base", []string{"messages:write", "messages:read", "cost:read"}, map[string]int{"eval": 403, "other": 403}},
		{"harness", []string{"messages:write", "messages:read", "cost:read", "routes:write:own_group"}, map[string]int{"eval": 200, "other": 403}},
	} {
		h := NewServer(db, nil, nil, fakeVerifier{sub: "eval", scope: test.scope, folder: "eval"}, 0, "").Handler()
		for folder, want := range test.want {
			got := doJSON(t, h, http.MethodPatch, "/v1/groups/"+url.PathEscape(folder), "", map[string]string{"config": `{"harness":{"backend":"codex"}}`}).Code
			if got != want {
				t.Errorf("%s: PATCH %s = %d, want %d", test.name, folder, got, want)
			}
		}
	}
}
