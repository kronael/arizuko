package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
)

func TestGroupDetailReadForDashboard(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const folder = "team/child"
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	if err := db.store().SetGroupOpen(folder, false); err != nil {
		t.Fatal(err)
	}
	if err := db.store().SetGroupObserveWindow(folder, 12, 400); err != nil {
		t.Fatal(err)
	}
	h := NewServer(db, nil, nil, nil, 0, "").Handler()
	w := doJSON(t, h, "GET", "/v1/groups/"+url.PathEscape(folder), "", nil)
	var g core.Group
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &g) != nil || g.Folder != folder || g.Open || g.ObserveWindowMessages != 12 || g.ObserveWindowChars != 400 {
		t.Fatalf("dashboard group detail = %d %s", w.Code, w.Body.String())
	}
	if err := db.PutGroup(core.Group{Folder: "flat"}); err != nil {
		t.Fatal(err)
	}
	if !resregtest.AssertDocumentedResponse(t, "routd", h.(*http.ServeMux), httptest.NewRequest(http.MethodGet, "/v1/groups/flat", nil)) {
		t.Fatal("group detail has no documented response")
	}
}

func TestGroupDetailScopedRESTAndMCP(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, folder := range []string{"team/child", "other"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewServer(db, nil, nil, fakeVerifier{sub: "alice", scope: []string{"routes:read"}, folder: "team"}, 0, "").Handler()
	get := func(folder string) int {
		return doJSON(t, h, http.MethodGet, "/v1/groups/"+url.PathEscape(folder), "", nil).Code
	}
	if code := get("team/child"); code != http.StatusOK {
		t.Fatalf("child = %d", code)
	}
	if code := get("team/missing"); code != http.StatusNotFound {
		t.Fatalf("missing = %d", code)
	}
	if code := get("other"); code != http.StatusForbidden {
		t.Fatalf("other = %d", code)
	}
	list := doJSON(t, h, http.MethodGet, "/v1/groups", "", nil)
	var rows []map[string]any
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &rows) != nil || len(rows) != 1 || len(rows[0]) != 1 || rows[0]["folder"] != "team/child" {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	grantMCPTools(t, db, "team", "get_group")
	sock := serveGroupsMCP(t, db, "team", "folder:team", t.TempDir())
	if result, msg := callToolOverSock(t, sock, "get_group", map[string]any{"folder": "team/child"}); msg != "" || result["Folder"] != "team/child" {
		t.Fatalf("MCP child = %+v %q", result, msg)
	}
	if _, msg := callToolOverSock(t, sock, "get_group", map[string]any{"folder": "other"}); msg == "" {
		t.Fatal("MCP sibling read allowed")
	}
}

func TestGroupDetailMetadata(t *testing.T) {
	r := resreg.Lookup(resources.GroupsName)
	if r == nil || r.Responses[resreg.ActionGet] == nil || r.MCPNames[resreg.ActionGet] != "get_group" {
		t.Fatalf("metadata = %+v", r)
	}
	var doc map[string]any
	if err := json.Unmarshal(routdDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	if paths["/v1/groups/{folder}"] == nil {
		t.Fatal("detail missing from OpenAPI")
	}
}
