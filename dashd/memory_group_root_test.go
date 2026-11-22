package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestMemoryGroupRootSymlinkConfined(t *testing.T) {
	d, folder, mux := setupNestedMemoryTest(t)
	err := d.routdStore().AddACLRow(core.ACLRow{
		Principal: "admin@x", Action: "admin", Scope: folder + "/**", Effect: "allow",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/child"} {
		victim := filepath.Join(d.groupsDir, "victim"+suffix)
		if err := os.MkdirAll(victim, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(victim, "MEMORY.md"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../../victim", filepath.Join(d.groupsDir, folder, "link")); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/child"} {
		for _, method := range []string{"PUT", "DELETE"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, authMemReq(method, "/dash/memory/"+folder+"/link"+suffix+"/MEMORY.md", "overwritten"))
			data, err := os.ReadFile(filepath.Join(d.groupsDir, "victim"+suffix, "MEMORY.md"))
			if w.Code != http.StatusForbidden || err != nil || string(data) != "private" {
				t.Fatalf("%s %s: HTTP=%d file=%q err=%v", method, suffix, w.Code, data, err)
			}
		}
	}
}
