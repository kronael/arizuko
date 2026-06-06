package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// openOwnedDB (split-mode) creates onbod.db at a fresh path with onbod's OWNED
// tables migrated in — NOT messages.db. The migrator never runs store's
// migrations against it, so a store-only table (e.g. messages) must be absent.
func TestOpenOwnedDB_SplitOpensOnbodDBNotMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()

	// owned tables exist
	for _, tbl := range []string{"onboarding", "invites", "onboarding_gates", "audit_log"} {
		var n int
		if err := db.QueryRow(
			`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Errorf("onbod.db missing owned table %q: %v", tbl, err)
		}
	}
	// messages.db-only table must NOT exist (onbod.db is its own file, not messages.db)
	var msgTbl string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&msgTbl)
	if err == nil {
		t.Fatalf("onbod.db must not carry the messages table (it is not messages.db)")
	}
}

// admin invite lifecycle against a real onbod.db: create → list → delete,
// nil KeySet (open, monolith/local-dev) so no bearer needed.
func TestAdminInviteCreateListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()
	a := &admin{db: db, ks: nil}

	// create
	body := `{"target_glob":"main/","max_uses":3}`
	w := httptest.NewRecorder()
	a.handleInviteCreate(w, httptest.NewRequest("POST", "/v1/invites", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created inviteJSON
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Token == "" || created.TargetGlob != "main/" || created.MaxUses != 3 {
		t.Fatalf("create returned wrong invite: %+v", created)
	}

	// list
	w = httptest.NewRecorder()
	a.handleInviteList(w, httptest.NewRequest("GET", "/v1/invites", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d", w.Code)
	}
	var listed struct {
		Invites []inviteJSON `json:"invites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Invites) != 1 || listed.Invites[0].Token != created.Token {
		t.Fatalf("list = %+v, want the one created invite", listed.Invites)
	}

	// delete
	req := httptest.NewRequest("DELETE", "/v1/invites/"+created.Token, nil)
	req.SetPathValue("token", created.Token)
	w = httptest.NewRecorder()
	a.handleInviteRevoke(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status=%d", w.Code)
	}

	// gone
	w = httptest.NewRecorder()
	a.handleInviteList(w, httptest.NewRequest("GET", "/v1/invites", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Invites) != 0 {
		t.Fatalf("after delete list = %+v, want empty", listed.Invites)
	}
}

// admin gate lifecycle: put (limit) → list → disable → delete.
func TestAdminGatePutListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()
	a := &admin{db: db, ks: nil}

	put := func(gate, body string) {
		req := httptest.NewRequest("PUT", "/v1/gates/"+gate, strings.NewReader(body))
		req.SetPathValue("gate", gate)
		w := httptest.NewRecorder()
		a.handleGatePut(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("put gate %s status=%d body=%s", gate, w.Code, w.Body.String())
		}
	}
	put("github:org=acme", `{"limit_per_day":25}`)

	w := httptest.NewRecorder()
	a.handleGateList(w, httptest.NewRequest("GET", "/v1/gates", nil))
	var listed struct {
		Gates []gateJSON `json:"gates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode gates: %v", err)
	}
	if len(listed.Gates) != 1 || listed.Gates[0].Gate != "github:org=acme" ||
		listed.Gates[0].LimitPerDay != 25 || !listed.Gates[0].Enabled {
		t.Fatalf("gate list wrong: %+v", listed.Gates)
	}

	// disable
	put("github:org=acme", `{"enabled":false}`)
	w = httptest.NewRecorder()
	a.handleGateList(w, httptest.NewRequest("GET", "/v1/gates", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if listed.Gates[0].Enabled {
		t.Fatalf("gate still enabled after disable")
	}

	// delete
	req := httptest.NewRequest("DELETE", "/v1/gates/github:org=acme", nil)
	req.SetPathValue("gate", "github:org=acme")
	w = httptest.NewRecorder()
	a.handleGateDelete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete gate status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	a.handleGateList(w, httptest.NewRequest("GET", "/v1/gates", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Gates) != 0 {
		t.Fatalf("gate not deleted: %+v", listed.Gates)
	}
}
