package routd

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
)

// spec 5/16: the /v1/groups REST list is scoped to the caller's subtree (closes
// the rest_listall leak), while the agent MCP refresh_groups stays unscoped.
func TestGroupsList_RESTScopedMCPUnscoped(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, f := range []string{"acme", "acme/eng", "other", "other/x"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(db, nil, nil, nil, 0, "https://x.test")

	list := func(surface, folder string) []string {
		res, err := srv.groupsHandler(context.Background(), resreg.Execution{
			Action:  resreg.ActionList,
			Surface: surface,
			Caller:  resreg.Caller{Folder: folder},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range res.([]struct {
			Folder string `json:"folder"`
		}) {
			out = append(out, g.Folder)
		}
		return out
	}

	// REST caller scoped to acme sees only acme + acme/eng, never other/*.
	rest := list(audit.SurfaceREST, "acme")
	for _, f := range rest {
		if f != "acme" && f != "acme/eng" {
			t.Fatalf("REST leak: acme caller saw %q (full: %v)", f, rest)
		}
	}
	if len(rest) != 2 {
		t.Fatalf("REST acme caller want 2 folders, got %v", rest)
	}

	// Agent MCP face is unscoped — sees the whole tree for delegation discovery.
	if mcp := list(audit.SurfaceMCP, "acme"); len(mcp) != 4 {
		t.Fatalf("MCP refresh_groups want all 4 folders, got %v", mcp)
	}
}
