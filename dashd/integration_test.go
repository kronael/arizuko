package main

// Integration tests for dashd. dashd intentionally has no auth (proxyd
// fronts it — see TestDashNoAuthGate in main_test.go), so no JWT cookie
// is minted here. These tests seed DB rows + files, then hit the mux.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/tests/testutils"
)

func newDashServer(t *testing.T) (*httptest.Server, *testutils.Inst, string) {
	t.Helper()
	inst := testutils.NewInstance(t)
	groupsDir := filepath.Join(inst.Tmp, "groups")
	if err := os.MkdirAll(groupsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbPath: "memory", groupsDir: groupsDir}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, inst, groupsDir
}

// TestMemoryEndpoint: seed a group with MEMORY.md on disk + a group row
// in DB, GET /dash/memory/?group=<folder>, assert HTML contains the
// memory content.
func TestMemoryEndpoint(t *testing.T) {
	srv, inst, groupsDir := newDashServer(t)

	folder := "alice"
	if _, err := inst.DB.Exec(
		`INSERT INTO groups (folder, name, added_at) VALUES (?, ?, ?)`,
		folder, folder, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(groupsDir, folder), 0o755); err != nil {
		t.Fatal(err)
	}
	const memContent = "MEMORY-CONTENT-MARKER-abc123"
	if err := os.WriteFile(
		filepath.Join(groupsDir, folder, "MEMORY.md"),
		[]byte(memContent), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/dash/memory/?group=" + folder)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	bs := string(body)
	if !strings.Contains(bs, memContent) {
		t.Errorf("response missing memory content; body len=%d", len(bs))
	}
	// Group appears in the dropdown as well.
	if !strings.Contains(bs, `value="`+folder+`"`) {
		t.Errorf("group dropdown missing folder %q", folder)
	}
}

// TestGroupList: seed two groups, GET /dash/groups/, assert both render.
func TestGroupList(t *testing.T) {
	srv, inst, _ := newDashServer(t)

	now := time.Now().Format(time.RFC3339)
	for _, f := range []string{"alpha", "bravo"} {
		if _, err := inst.DB.Exec(
			`INSERT INTO groups (folder, name, added_at) VALUES (?, ?, ?)`,
			f, f, now); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(srv.URL + "/dash/groups/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	bs := string(body)
	for _, f := range []string{"alpha", "bravo"} {
		if !strings.Contains(bs, f) {
			t.Errorf("group %q missing from /dash/groups/ body", f)
		}
	}
}

// TestTaskList: seed scheduled_tasks rows, GET /dash/tasks/, assert rows
// render in the full page and in the HTMX partial.
func TestTaskList(t *testing.T) {
	srv, inst, _ := newDashServer(t)

	now := time.Now().Format(time.RFC3339)
	tasks := []struct {
		id, owner, cron string
	}{
		{"task-one", "alice", "*/5 * * * *"},
		{"task-two", "bob", "0 9 * * *"},
	}
	for _, tk := range tasks {
		if _, err := inst.DB.Exec(
			`INSERT INTO scheduled_tasks
			 (id, owner, chat_jid, prompt, cron, status, created_at)
			 VALUES (?, ?, ?, ?, ?, 'active', ?)`,
			tk.id, tk.owner, "local:"+tk.owner, "do stuff", tk.cron, now); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(srv.URL + "/dash/tasks/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	bs := string(body)
	for _, tk := range tasks {
		if !strings.Contains(bs, tk.id) {
			t.Errorf("task id %q missing from /dash/tasks/", tk.id)
		}
		if !strings.Contains(bs, tk.owner) {
			t.Errorf("task owner %q missing from /dash/tasks/", tk.owner)
		}
	}

	// HTMX partial renders the same rows without the page chrome.
	respP, err := http.Get(srv.URL + "/dash/tasks/x/list")
	if err != nil {
		t.Fatal(err)
	}
	pbody, _ := io.ReadAll(respP.Body)
	respP.Body.Close()
	if respP.StatusCode != http.StatusOK {
		t.Fatalf("partial status = %d", respP.StatusCode)
	}
	pbs := string(pbody)
	for _, tk := range tasks {
		if !strings.Contains(pbs, tk.id) {
			t.Errorf("task id %q missing from tasks partial", tk.id)
		}
	}
}
