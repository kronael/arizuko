package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
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

// admin gate lifecycle through the resreg /v1/gates face (spec 5/44 fold):
// put (limit) → list → disable → re-put limit → delete. Drives a real mux so
// {gate} path binding + resreg dispatch are exercised end-to-end; ks=nil (open)
// so no bearer needed.
func TestAdminGatePutListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()
	mux := http.NewServeMux()
	(&admin{db: db, ks: nil}).mountGates(mux)

	do := func(method, target, body string) *httptest.ResponseRecorder {
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, rdr)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	list := func() []gateJSON {
		w := do("GET", "/v1/gates", "")
		if w.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
		}
		var listed struct {
			Gates []gateJSON `json:"gates"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode gates: %v", err)
		}
		return listed.Gates
	}
	put := func(gate, body string) {
		w := do("PUT", "/v1/gates/"+gate, body)
		if w.Code != http.StatusOK {
			t.Fatalf("put gate %s status=%d body=%s", gate, w.Code, w.Body.String())
		}
	}

	put("github:org=acme", `{"limit_per_day":25}`)
	if g := list(); len(g) != 1 || g[0].Gate != "github:org=acme" ||
		g[0].LimitPerDay != 25 || !g[0].Enabled {
		t.Fatalf("gate list wrong: %+v", g)
	}

	// tri-state: enabled-only PUT flips the flag and leaves the limit untouched.
	put("github:org=acme", `{"enabled":false}`)
	if g := list(); g[0].Enabled || g[0].LimitPerDay != 25 {
		t.Fatalf("enabled-only put should disable but keep limit 25: %+v", g)
	}

	// tri-state: limit-only PUT upserts the limit and leaves enablement untouched.
	put("github:org=acme", `{"limit_per_day":40}`)
	if g := list(); g[0].Enabled || g[0].LimitPerDay != 40 {
		t.Fatalf("limit-only put should keep disabled but set limit 40: %+v", g)
	}

	// delete → {ok:true}, then gone.
	if w := do("DELETE", "/v1/gates/github:org=acme", ""); w.Code != http.StatusOK {
		t.Fatalf("delete gate status=%d body=%s", w.Code, w.Body.String())
	}
	if g := list(); len(g) != 0 {
		t.Fatalf("gate not deleted: %+v", g)
	}
}

// gatesRESTGate reproduces the retired handlers' any-of scope check: gates:read
// OR gates:write lists; only gates:write mutates. Non-nil ks makes it enforce
// (the gate itself never touches ks, so an empty KeySet suffices). nil ks is
// covered by TestAdminGatePutListDelete (open path).
func TestGatesRESTGateScopes(t *testing.T) {
	a := &admin{ks: &auth.KeySet{}}
	call := func(action resreg.Action, scopes string) error {
		return a.gatesRESTGate(resreg.Execution{
			Action: action,
			Caller: resreg.Caller{Claims: map[string]string{"scopes": scopes}},
		}, "", nil)
	}
	cases := []struct {
		action resreg.Action
		scopes string
		ok     bool
	}{
		{resreg.ActionList, "gates:read", true},
		{resreg.ActionList, "gates:write", true}, // write covers read
		{resreg.ActionList, "invites:read", false},
		{resreg.ActionUpdate, "gates:write", true},
		{resreg.ActionUpdate, "gates:read", false}, // read cannot mutate
		{resreg.ActionUpdate, "", false},
		{resreg.ActionDelete, "gates:write", true},
		{resreg.ActionDelete, "gates:read", false},
	}
	for _, c := range cases {
		err := call(c.action, c.scopes)
		if (err == nil) != c.ok {
			t.Errorf("%s scopes=%q: got err=%v, want ok=%v", c.action, c.scopes, err, c.ok)
		}
	}
}
