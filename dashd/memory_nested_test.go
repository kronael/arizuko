package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

// setupNestedMemoryTest wires a dash whose group folder is nested ("corp/eng"),
// with admin@x granted admin on exactly that folder.
func setupNestedMemoryTest(t *testing.T) (*dash, string, *http.ServeMux) {
	t.Helper()
	groups := t.TempDir()
	folder := "corp/eng"
	if err := os.MkdirAll(filepath.Join(groups, folder, "diary"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := testutils.NewRoutdInstance(t)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "admin@x", Action: "admin", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	d := &dash{routd: inst.Store.Obj(), groupsDir: groups, routdURL: stubRoutd(t, inst.Store.Obj())}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	return d, folder, mux
}

// parseMemoryPath splits /dash/memory/<folder>/<rel> by scanning candidate split
// points, so a nested folder still resolves. Splitting on the FIRST "/" gave
// folder="corp", rel="eng/MEMORY.md", which failed the rel allowlist and made
// every mutation on a nested group a 400 (BUGS "main: memory write/delete broken
// for nested group folders", 2026-06-18).
func TestParseMemoryPath_nestedFolder(t *testing.T) {
	for _, tc := range []struct{ path, folder, rel string }{
		{"/dash/memory/g1/MEMORY.md", "g1", "MEMORY.md"},
		{"/dash/memory/corp/eng/MEMORY.md", "corp/eng", "MEMORY.md"},
		{"/dash/memory/corp/eng/sre/PERSONA.md", "corp/eng/sre", "PERSONA.md"},
		{"/dash/memory/corp/eng/diary/2026-04-19.md", "corp/eng", "diary/2026-04-19.md"},
		{"/dash/memory/corp/eng/.claude/CLAUDE.md", "corp/eng", ".claude/CLAUDE.md"},
		{"/dash/memory/corp/eng/random.md", "", ""},
		{"/dash/memory/corp/eng/../../etc/passwd", "", ""},
	} {
		folder, rel := parseMemoryPath(tc.path)
		if folder != tc.folder || rel != tc.rel {
			t.Errorf("parseMemoryPath(%q) = (%q, %q), want (%q, %q)",
				tc.path, folder, rel, tc.folder, tc.rel)
		}
	}
}

func TestMemoryWrite_nestedFolder(t *testing.T) {
	d, folder, mux := setupNestedMemoryTest(t)

	body := "nested memory\n"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, authMemReq("PUT", "/dash/memory/"+folder+"/MEMORY.md", body))
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT nested MEMORY.md = %d, body = %q", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(d.groupsDir, folder, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Errorf("file contents = %q, want %q", data, body)
	}
}

func TestMemoryWriteDiary_nestedFolder(t *testing.T) {
	d, folder, mux := setupNestedMemoryTest(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, authMemReq("PUT", "/dash/memory/"+folder+"/diary/2026-04-19.md", "entry"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT nested diary = %d, body = %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(d.groupsDir, folder, "diary", "2026-04-19.md")); err != nil {
		t.Fatalf("nested diary file missing: %v", err)
	}
}

func TestMemoryDelete_nestedFolder(t *testing.T) {
	d, folder, mux := setupNestedMemoryTest(t)
	path := filepath.Join(d.groupsDir, folder, "MEMORY.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, authMemReq("DELETE", "/dash/memory/"+folder+"/MEMORY.md", ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE nested MEMORY.md = %d, body = %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be deleted, got err = %v", err)
	}
}

// The nested path still binds to the parsed folder: a caller with no grant on
// corp/eng is refused, so the scan cannot be used to reach another tenant.
func TestMemoryWrite_nestedFolderDeniesUngrantedCaller(t *testing.T) {
	_, folder, mux := setupNestedMemoryTest(t)

	req := authMemReq("PUT", "/dash/memory/"+folder+"/MEMORY.md", "x")
	req.Header.Set("X-User-Sub", "mallory@x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT nested MEMORY.md as ungranted caller = %d, want 403", w.Code)
	}
}
