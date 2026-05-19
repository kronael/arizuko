package main

// Integration tests for admin-gated CRUD shipped in TIER 1: routes
// create/delete and group delete. Seeds an acl admin row, drives a
// real HTTP request through the mux with dbRW wired so writes land.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

// newRWDashServer mirrors newDashServer but wires dbRW so admin writes
// run. Returns server + the testutils Inst (Store + DB).
func newRWDashServer(t *testing.T) (*httptest.Server, *testutils.Inst, string) {
	t.Helper()
	inst := testutils.NewInstance(t)
	groupsDir := filepath.Join(inst.Tmp, "groups")
	if err := os.MkdirAll(groupsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbRW: inst.DB, dbPath: "memory", groupsDir: groupsDir}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, inst, groupsDir
}

func noFollow() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// TestDash_RouteCreateAndDelete: admin POSTs a route, then deletes it.
func TestDash_RouteCreateAndDelete(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"seq": {"10"}, "match": {"room=42"}, "target": {"world/sub"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/routes/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	var got core.Route
	for _, r := range s.AllRoutes() {
		if r.Match == "room=42" {
			got = r
			break
		}
	}
	if got.ID == 0 || got.Target != "world/sub" {
		t.Fatalf("route not stored: %+v", s.AllRoutes())
	}

	delURL := srv.URL + "/dash/routes/" + strconv.FormatInt(got.ID, 10) + "/delete"
	req, _ = http.NewRequest("POST", delURL, nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err = noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	for _, r := range s.AllRoutes() {
		if r.ID == got.ID {
			t.Errorf("route %d still present after delete", got.ID)
		}
	}
}

// TestDash_RouteCreate_DenyNonAdmin: requireAdmin gates the POST.
func TestDash_RouteCreate_DenyNonAdmin(t *testing.T) {
	srv, _, _ := newRWDashServer(t)
	form := url.Values{"seq": {"0"}, "match": {"room=1"}, "target": {"world/sub"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/routes/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "stranger@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}


// TestDash_GroupDelete: admin deletes a group via POST-alias.
func TestDash_GroupDelete(t *testing.T) {
	srv, inst, groupsDir := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	// {folder} pattern in the mux is a single path segment; use a flat name.
	folder := "worldsub"
	if err := s.PutGroup(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	groupDir := filepath.Join(groupsDir, folder)
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/delete", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete status = %d, body deduce", resp.StatusCode)
	}
	if _, ok := s.AllGroups()[folder]; ok {
		t.Errorf("group row still present")
	}
	if _, err := os.Stat(groupDir); !os.IsNotExist(err) {
		t.Errorf("group dir still present: %v", err)
	}
}

// TestDash_GrantsView: admin GETs the grants page, sees the row.
func TestDash_GrantsView(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "world"
	if err := inst.DB.QueryRow(`SELECT 1`).Err(); err != nil {
		t.Fatal(err)
	}
	// Seed a grant row for this folder.
	if err := s.AddACLRow(core.ACLRow{
		Principal: "bob@x", Action: "send", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/dash/groups/"+folder+"/grants", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET grants status = %d", resp.StatusCode)
	}
}

// TestDash_GrantAdd: admin adds a grant, it appears in ListACLByScope.
func TestDash_GrantAdd(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "world"

	form := url.Values{
		"principal": {"carol@x"},
		"action":    {"reply"},
		"effect":    {"allow"},
		"params":    {""},
		"scope":     {folder},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/grants",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add grant status = %d", resp.StatusCode)
	}

	rows := s.ListACLByScope(folder)
	found := false
	for _, r := range rows {
		if r.Principal == "carol@x" && r.Action == "reply" {
			found = true
		}
	}
	if !found {
		t.Errorf("grant row not found after add; rows: %+v", rows)
	}
}

// TestDash_GrantRevoke: admin adds then revokes a grant.
func TestDash_GrantRevoke(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "world"
	if err := s.AddACLRow(core.ACLRow{
		Principal: "dave@x", Action: "send", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"principal": {"dave@x"},
		"action":    {"send"},
		"effect":    {"allow"},
		"params":    {""},
		"predicate": {""},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/grants/revoke",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke status = %d", resp.StatusCode)
	}

	rows := s.ListACLByScope(folder)
	for _, r := range rows {
		if r.Principal == "dave@x" && r.Action == "send" {
			t.Errorf("grant still present after revoke")
		}
	}
}

// TestDash_GrantAdd_DenyNonAdmin: non-admin cannot add grants.
func TestDash_GrantAdd_DenyNonAdmin(t *testing.T) {
	srv, _, _ := newRWDashServer(t)
	form := url.Values{"principal": {"x"}, "action": {"send"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/world/grants",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "stranger@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}
