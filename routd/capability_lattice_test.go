package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// A folder that holds `admin`, `mcp:*` or `*` on itself holds no guest
// capability: its share stays writable and it gets no egress, web surface or
// GitHub write. Only a row that names the capability confers it.
func TestDispatchLatticeGrantsNoCapability(t *testing.T) {
	for _, action := range []string{"admin", "mcp:*", "*"} {
		t.Run(action, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_ = db.PutGroup(core.Group{Folder: "demo"})
			if err := grantACL(db, core.ACLRow{Principal: "folder:demo", Scope: "demo", Action: action, GrantedBy: "test"}); err != nil {
				t.Fatalf("grant: %v", err)
			}
			req := dispatchOnce(t, db)
			if req.ShareReadOnly || req.Egress || req.WebPublish || req.GHWrite {
				t.Errorf("share_readonly=%v egress=%v web_publish=%v gh_write=%v, want all false",
					req.ShareReadOnly, req.Egress, req.WebPublish, req.GHWrite)
			}
		})
	}
}

// A row that names a guest capability confers it, and a deny on the same
// action takes it away.
func TestDispatchReadsNamedCapabilities(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	for _, row := range []core.ACLRow{
		{Action: "mcp:share_mount", Params: "readonly=true"},
		{Action: "egress"},
		{Action: "web:publish"},
		{Action: "gh:write"},
		{Action: "gh:write", Effect: "deny"},
	} {
		row.Principal, row.Scope, row.GrantedBy = "folder:demo", "demo", "test"
		if err := grantACL(db, row); err != nil {
			t.Fatalf("grant %s: %v", row.Action, err)
		}
	}
	req := dispatchOnce(t, db)
	if !req.ShareReadOnly || !req.Egress || !req.WebPublish || req.GHWrite {
		t.Errorf("share_readonly=%v egress=%v web_publish=%v gh_write=%v, want true true true false",
			req.ShareReadOnly, req.Egress, req.WebPublish, req.GHWrite)
	}
}
