package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestGroupDelete_RemovesNothing is BUGS L669: admin on one folder does not
// prove the right to remove a deny that lives under it and binds elsewhere, so
// the dashboard purges nothing and names the host command that does. Every
// item the old purge touched is read back: the group, its subtree, an acl row
// under it and the membership edges of both folders.
func TestGroupDelete_RemovesNothing(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolder(t, routd, "team/child")
	seedGrant(t, routd, core.ACLRow{
		Principal: "google:bob", Action: "mcp:send", Scope: "team/**", Effect: "deny",
	})
	st := store.NewOver(routd)

	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))

	if w.Code != http.StatusConflict {
		t.Fatalf("POST delete = %d, want 409 body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "arizuko group -i") ||
		!strings.Contains(w.Body.String(), " rm team") {
		t.Errorf("body does not name the host command: %q", w.Body.String())
	}
	for _, f := range []string{"team", "team/child"} {
		if !hasFolder(t, routd, f) {
			t.Errorf("groups row %s was removed", f)
		}
		if edges, err := st.ParentEdges("folder:" + f); err != nil || len(edges) == 0 {
			t.Errorf("membership edge of %s was removed: %v %v", f, edges, err)
		}
	}
	if rows, err := st.ListACLByScope("team/**"); err != nil || len(rows) != 1 {
		t.Errorf("acl row under the folder was removed: %v %v", rows, err)
	}
}

// A caller without admin on the folder is still refused with 403, and is not
// told the command.
func TestGroupDelete_StillNeedsAdmin(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "stranger@x"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST delete by a stranger = %d, want 403", w.Code)
	}
}
